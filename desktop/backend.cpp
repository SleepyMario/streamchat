#include "backend.h"

#include <QCoreApplication>
#include <QDesktopServices>
#include <QDir>
#include <QFileInfo>
#include <QGuiApplication>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QNetworkReply>
#include <QNetworkRequest>
#include <QRegularExpression>
#include <QSettings>
#include <QTcpSocket>

namespace {
QVariantMap objectMap(const QByteArray &data, QString *error = nullptr)
{
    QJsonParseError parseError;
    const QJsonDocument document = QJsonDocument::fromJson(data, &parseError);
    if (parseError.error != QJsonParseError::NoError || !document.isObject()) {
        if (error)
            *error = parseError.errorString();
        return {};
    }
    return document.object().toVariantMap();
}
}

StreamchatBackend::StreamchatBackend(QObject *parent) : QObject(parent)
{
    m_baseFont = QGuiApplication::font();
    QSettings settings;
    const double savedScale = settings.value(QStringLiteral("ui/fontScale"), 1.0).toDouble();
    m_connectionMode = settings.value(QStringLiteral("connection/mode"), QStringLiteral("local")).toString();
    m_serverUrl = settings.value(QStringLiteral("connection/serverUrl")).toString();
    setFontScale(savedScale);
    m_runtimeProbe.setInterval(250);
    connect(&m_runtimeProbe, &QTimer::timeout, this, &StreamchatBackend::checkRuntime);
    m_reconnect.setSingleShot(true);
    m_reconnect.setInterval(1500);
    connect(&m_reconnect, &QTimer::timeout, this, &StreamchatBackend::connectEvents);
}

StreamchatBackend::~StreamchatBackend()
{
	stopRuntime();
}

QUrl StreamchatBackend::endpoint(const QString &path) const
{
    return m_baseUrl.resolved(QUrl(path.startsWith('/') ? path.mid(1) : path));
}

void StreamchatBackend::start()
{
    m_probeAttempts = 0;
    QTcpSocket socket;
    socket.connectToHost(QHostAddress::LocalHost, 8792);
    if (socket.waitForConnected(250)) {
        socket.disconnectFromHost();
        checkRuntime();
    } else {
        launchRuntime();
    }
}

void StreamchatBackend::checkRuntime()
{
    QNetworkRequest request(endpoint(QStringLiteral("/healthz")));
    request.setTransferTimeout(1000);
    QNetworkReply *reply = m_network.get(request);
    connect(reply, &QNetworkReply::finished, this, [this, reply] {
        const bool healthy = reply->error() == QNetworkReply::NoError;
        reply->deleteLater();
        if (healthy) {
            m_runtimeProbe.stop();
            setConnected(true);
            refreshState();
            connectEvents();
            return;
        }
        if (!m_ownsRuntime) {
            launchRuntime();
            return;
        }
        if (++m_probeAttempts >= 40) {
            m_runtimeProbe.stop();
            setNotice(QStringLiteral("The Streamchat runtime did not start."), true);
        }
    });
}

void StreamchatBackend::launchRuntime()
{
    const QDir directory(QCoreApplication::applicationDirPath());
    QString runtimeName = QStringLiteral("streamchat-gui-runtime");
#ifdef Q_OS_WIN
    runtimeName += QStringLiteral(".exe");
#endif
    const QString runtime = directory.filePath(runtimeName);
    if (!QFileInfo::exists(runtime)) {
        setNotice(QStringLiteral("streamchat-gui-runtime was not found beside the application."), true);
        return;
    }
    m_ownsRuntime = true;
    m_runtime.setProgram(runtime);
    QStringList arguments{QStringLiteral("--no-open"), QStringLiteral("--listen"), QStringLiteral("127.0.0.1:8792"),
                          QStringLiteral("--connection"), m_connectionMode};
    if (m_connectionMode == QStringLiteral("remote") && !m_serverUrl.trimmed().isEmpty())
        arguments << QStringLiteral("--server-url") << m_serverUrl.trimmed();
    m_runtime.setArguments(arguments);
    m_runtime.setProcessChannelMode(QProcess::ForwardedErrorChannel);
    m_runtime.start();
    if (!m_runtime.waitForStarted(1500)) {
        setNotice(QStringLiteral("Could not start the Streamchat runtime."), true);
        return;
    }
    m_probeAttempts = 0;
    m_runtimeProbe.start();
}

void StreamchatBackend::stopRuntime()
{
    if (!m_ownsRuntime)
        return;

    QTcpSocket socket;
    socket.connectToHost(QHostAddress::LocalHost, 8792);
    if (socket.waitForConnected(500)) {
        const QByteArray request = "POST /api/shutdown HTTP/1.1\r\n"
                                   "Host: 127.0.0.1:8792\r\n"
                                   "Content-Type: application/json\r\n"
                                   "Content-Length: 2\r\n"
                                   "Connection: close\r\n\r\n{}";
        socket.write(request);
        socket.waitForBytesWritten(500);
        socket.disconnectFromHost();
    }
    if (!m_runtime.waitForFinished(3000)) {
        m_runtime.kill();
        m_runtime.waitForFinished(1000);
    }
    m_ownsRuntime = false;
}

void StreamchatBackend::refreshState()
{
    getJson(QStringLiteral("/api/state"), [this](const QVariantMap &result) { applyState(result); });
}

void StreamchatBackend::connectEvents()
{
    if (m_events)
        return;
    QNetworkRequest request(endpoint(QStringLiteral("/events")));
    request.setRawHeader("Accept", "text/event-stream");
    request.setRawHeader("Cache-Control", "no-cache");
    m_events = m_network.get(request);
    connect(m_events, &QNetworkReply::readyRead, this, [this] {
        m_eventBuffer += m_events->readAll();
        m_eventBuffer.replace("\r\n", "\n");
        qsizetype boundary;
        while ((boundary = m_eventBuffer.indexOf("\n\n")) >= 0) {
            const QByteArray block = m_eventBuffer.left(boundary);
            m_eventBuffer.remove(0, boundary + 2);
            processEventBlock(block);
        }
    });
    connect(m_events, &QNetworkReply::finished, this, [this] {
        if (m_events) {
            m_events->deleteLater();
            m_events = nullptr;
        }
        setConnected(false);
        if (!m_reconnect.isActive())
            m_reconnect.start();
    });
}

void StreamchatBackend::processEventBlock(const QByteArray &block)
{
    QByteArray eventName;
    QByteArray data;
    for (const QByteArray &line : block.split('\n')) {
        if (line.startsWith("event:"))
            eventName = line.mid(6).trimmed();
        else if (line.startsWith("data:")) {
            if (!data.isEmpty())
                data += '\n';
            data += line.mid(5).trimmed();
        }
    }
    if (eventName == "status") {
        setConnected(true);
        return;
    }
    if (data.isEmpty())
        return;
    const QVariantMap value = objectMap(data);
    if (eventName == "state")
        applyState(value);
    else if (eventName == "message")
        appendMessage(value);
}

void StreamchatBackend::applyState(const QVariantMap &state)
{
    m_state = state;
    emit stateChanged();
}

QString StreamchatBackend::displayText(const QVariantMap &message)
{
    const QString text = message.value(QStringLiteral("text")).toString();
    QList<uint> runes = text.toUcs4();
    QVector<bool> hidden(runes.size(), false);
    const QVariantList emotes = message.value(QStringLiteral("emotes")).toList();
    for (const QVariant &item : emotes) {
        const QVariantMap emote = item.toMap();
        if (emote.value(QStringLiteral("url")).toString().isEmpty())
            continue;
        const int start = emote.value(QStringLiteral("start"), 0).toInt();
        const int end = emote.value(QStringLiteral("end"), -1).toInt();
        if (start < 0 || end < start || end >= runes.size())
            continue;
        for (int index = start; index <= end; ++index)
            hidden[index] = true;
    }
    QList<uint> visible;
    visible.reserve(runes.size());
    for (qsizetype index = 0; index < runes.size(); ++index) {
        if (!hidden[index])
            visible.append(runes[index]);
    }
    QString result = QString::fromUcs4(reinterpret_cast<const char32_t *>(visible.constData()), visible.size());
    result.replace(QRegularExpression(QStringLiteral("[ \\t]+")), QStringLiteral(" "));
    result.replace(QRegularExpression(QStringLiteral(" *\\n *")), QStringLiteral("\n"));
    return result.trimmed();
}

void StreamchatBackend::appendMessage(QVariantMap message)
{
    message.insert(QStringLiteral("display_text"), displayText(message));
    m_messages.append(message);
    while (m_messages.size() > 500)
        m_messages.removeFirst();
    emit messagesChanged();
}

void StreamchatBackend::selectTarget(const QString &platform)
{
    postAction({{QStringLiteral("Action"), QStringLiteral("select")}, {QStringLiteral("Platform"), platform}},
               QStringLiteral("Outgoing target: %1").arg(platform));
}

void StreamchatBackend::sendMessage(const QString &text)
{
    const QString cleaned = text.trimmed();
    if (cleaned.isEmpty() || m_state.value(QStringLiteral("selected")).toString().isEmpty())
        return;
    postAction({{QStringLiteral("Action"), QStringLiteral("send")}, {QStringLiteral("Text"), cleaned}},
               QStringLiteral("Message sent"), [this](const QVariantMap &) { emit messageSent(); });
}

void StreamchatBackend::updateTitle(const QString &title)
{
    postAction({{QStringLiteral("Action"), QStringLiteral("title")}, {QStringLiteral("Title"), title}}, QStringLiteral("Title updated"));
}

void StreamchatBackend::updateCategory(const QString &category)
{
    postAction({{QStringLiteral("Action"), QStringLiteral("category")}, {QStringLiteral("Category"), category}}, QStringLiteral("Category updated"));
}

void StreamchatBackend::ban(const QString &platform, const QString &user)
{
    postAction({{QStringLiteral("Action"), QStringLiteral("ban")}, {QStringLiteral("Platform"), platform}, {QStringLiteral("User"), user}}, QStringLiteral("User banned"));
}

void StreamchatBackend::timeout(const QString &platform, const QString &user, const QString &duration)
{
    postAction({{QStringLiteral("Action"), QStringLiteral("timeout")}, {QStringLiteral("Platform"), platform}, {QStringLiteral("User"), user}, {QStringLiteral("Duration"), duration}}, QStringLiteral("User timed out"));
}

void StreamchatBackend::clearRemote(const QString &platform, int days)
{
    postAction({{QStringLiteral("Action"), QStringLiteral("clear")}, {QStringLiteral("Platform"), platform}, {QStringLiteral("Days"), days}}, QStringLiteral("Remote chat cleared"));
}

void StreamchatBackend::clearLocal()
{
    m_messages.clear();
    emit messagesChanged();
}

void StreamchatBackend::openStream(const QString &platform)
{
    postJson(QStringLiteral("/api/open"), {{QStringLiteral("platform"), platform}}, [this](const QVariantMap &result) {
        const QUrl url(result.value(QStringLiteral("url")).toString());
        if (url.isValid())
            QDesktopServices::openUrl(url);
    });
}

void StreamchatBackend::inspect(const QString &kind)
{
    QString path;
    if (kind == QStringLiteral("config")) {
        path = QStringLiteral("/api/config");
        m_dialogTitle = QStringLiteral("Configuration and health");
    } else if (kind == QStringLiteral("setup")) {
        path = QStringLiteral("/api/setup");
        m_dialogTitle = QStringLiteral("Setup and OAuth guidance");
    } else {
        path = QStringLiteral("/api/archive");
        m_dialogTitle = QStringLiteral("Archive statistics");
    }
    emit dialogChanged();
    QNetworkRequest request(endpoint(path));
    request.setTransferTimeout(20000);
    QNetworkReply *reply = m_network.get(request);
    connect(reply, &QNetworkReply::finished, this, [this, reply] {
        if (reply->error() != QNetworkReply::NoError) {
            setNotice(reply->errorString(), true);
            reply->deleteLater();
            return;
        }
        const QJsonDocument document = QJsonDocument::fromJson(reply->readAll());
        m_dialogText = QString::fromUtf8(document.toJson(QJsonDocument::Indented));
        emit dialogChanged();
        reply->deleteLater();
    });
}

void StreamchatBackend::shutdown()
{
    postJson(QStringLiteral("/api/shutdown"), {}, [](const QVariantMap &) { QCoreApplication::quit(); });
}

void StreamchatBackend::adjustFont(int steps)
{
    setFontScale(m_fontScale + (0.1 * steps));
}

void StreamchatBackend::resetFont()
{
    setFontScale(1.0);
}

void StreamchatBackend::configureConnection(const QString &mode, const QString &serverUrl)
{
    const QString normalizedMode = mode.trimmed().toLower();
    const QString normalizedUrl = serverUrl.trimmed();
    if (normalizedMode != QStringLiteral("local") && normalizedMode != QStringLiteral("remote")) {
        setNotice(QStringLiteral("Choose local or remote server mode."), true);
        return;
    }
    const QUrl remoteUrl(normalizedUrl);
    if (normalizedMode == QStringLiteral("remote") &&
        (!remoteUrl.isValid() || (remoteUrl.scheme() != QStringLiteral("ws") && remoteUrl.scheme() != QStringLiteral("wss")))) {
        setNotice(QStringLiteral("Enter a valid remote Streamchat server URL."), true);
        return;
    }
    m_connectionMode = normalizedMode;
    m_serverUrl = normalizedUrl;
    QSettings settings;
    settings.setValue(QStringLiteral("connection/mode"), m_connectionMode);
    settings.setValue(QStringLiteral("connection/serverUrl"), m_serverUrl);
    emit connectionChanged();

    m_reconnect.stop();
    m_runtimeProbe.stop();
    if (m_events) {
        disconnect(m_events, nullptr, this, nullptr);
        m_events->abort();
        m_events->deleteLater();
        m_events = nullptr;
    }
    stopRuntime();
    setConnected(false);
    m_probeAttempts = 0;
    checkRuntime();
    setNotice(normalizedMode == QStringLiteral("local") ? QStringLiteral("Using the built-in local server")
                                                         : QStringLiteral("Connecting to the remote server"));
}

void StreamchatBackend::postAction(const QVariantMap &payload, const QString &success, const JsonCallback &callback)
{
    setBusy(true);
    postJson(QStringLiteral("/api/action"), payload, [this, success, callback](const QVariantMap &result) {
        if (result.contains(QStringLiteral("state")))
            applyState(result.value(QStringLiteral("state")).toMap());
        setNotice(result.value(QStringLiteral("result"), success).toString());
        setBusy(false);
        if (callback)
            callback(result);
    });
}

void StreamchatBackend::getJson(const QString &path, const JsonCallback &callback)
{
    QNetworkRequest request(endpoint(path));
    request.setTransferTimeout(20000);
    QNetworkReply *reply = m_network.get(request);
    connect(reply, &QNetworkReply::finished, this, [this, reply, callback] { finishReply(reply, callback); });
}

void StreamchatBackend::postJson(const QString &path, const QVariantMap &payload, const JsonCallback &callback)
{
    QNetworkRequest request(endpoint(path));
    request.setHeader(QNetworkRequest::ContentTypeHeader, QStringLiteral("application/json"));
    request.setTransferTimeout(30000);
    const QByteArray body = QJsonDocument::fromVariant(payload).toJson(QJsonDocument::Compact);
    QNetworkReply *reply = m_network.post(request, body);
    connect(reply, &QNetworkReply::finished, this, [this, reply, callback] { finishReply(reply, callback); });
}

void StreamchatBackend::finishReply(QNetworkReply *reply, const JsonCallback &callback)
{
    const QByteArray data = reply->readAll();
    const QVariantMap result = objectMap(data);
    if (reply->error() != QNetworkReply::NoError) {
        const QString error = result.value(QStringLiteral("error"), reply->errorString()).toString();
        setNotice(error, true);
        setBusy(false);
        reply->deleteLater();
        return;
    }
    reply->deleteLater();
    if (callback)
        callback(result);
}

void StreamchatBackend::setNotice(const QString &text, bool error)
{
    m_notice = text;
    m_noticeError = error;
    emit noticeChanged();
}

void StreamchatBackend::setBusy(bool value)
{
    if (m_busy == value)
        return;
    m_busy = value;
    emit busyChanged();
}

void StreamchatBackend::setConnected(bool value)
{
    if (m_connected == value)
        return;
    m_connected = value;
    emit connectedChanged();
}

void StreamchatBackend::setFontScale(double value)
{
    value = qBound(0.7, value, 2.0);
    value = qRound(value * 10.0) / 10.0;
    if (!qFuzzyCompare(m_fontScale, value) || QGuiApplication::font() == m_baseFont) {
        m_fontScale = value;
        QFont font = m_baseFont;
        if (font.pointSizeF() > 0)
            font.setPointSizeF(font.pointSizeF() * m_fontScale);
        else if (font.pixelSize() > 0)
            font.setPixelSize(qRound(font.pixelSize() * m_fontScale));
        QGuiApplication::setFont(font);
        QSettings().setValue(QStringLiteral("ui/fontScale"), m_fontScale);
        emit fontScaleChanged();
    }
}

QString StreamchatBackend::prettyJson(const QVariant &value)
{
    return QString::fromUtf8(QJsonDocument::fromVariant(value).toJson(QJsonDocument::Indented));
}
