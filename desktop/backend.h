#pragma once

#include <QNetworkAccessManager>
#include <QObject>
#include <QProcess>
#include <QTimer>
#include <QUrl>
#include <QVariantList>
#include <QVariantMap>
#include <QFont>

#include <functional>

class QNetworkReply;

class StreamchatBackend final : public QObject
{
    Q_OBJECT
    Q_PROPERTY(QVariantMap state READ state NOTIFY stateChanged)
    Q_PROPERTY(QVariantList messages READ messages NOTIFY messagesChanged)
    Q_PROPERTY(QString notice READ notice NOTIFY noticeChanged)
    Q_PROPERTY(bool noticeError READ noticeError NOTIFY noticeChanged)
    Q_PROPERTY(bool connected READ connected NOTIFY connectedChanged)
    Q_PROPERTY(bool busy READ busy NOTIFY busyChanged)
    Q_PROPERTY(QString dialogTitle READ dialogTitle NOTIFY dialogChanged)
    Q_PROPERTY(QString dialogText READ dialogText NOTIFY dialogChanged)
    Q_PROPERTY(double fontScale READ fontScale NOTIFY fontScaleChanged)
    Q_PROPERTY(QString connectionMode READ connectionMode NOTIFY connectionChanged)
    Q_PROPERTY(QString serverUrl READ serverUrl NOTIFY connectionChanged)

public:
    explicit StreamchatBackend(QObject *parent = nullptr);
    ~StreamchatBackend() override;

    QVariantMap state() const { return m_state; }
    QVariantList messages() const { return m_messages; }
    QString notice() const { return m_notice; }
    bool noticeError() const { return m_noticeError; }
    bool connected() const { return m_connected; }
    bool busy() const { return m_busy; }
    QString dialogTitle() const { return m_dialogTitle; }
    QString dialogText() const { return m_dialogText; }
    double fontScale() const { return m_fontScale; }
    QString connectionMode() const { return m_connectionMode; }
    QString serverUrl() const { return m_serverUrl; }

    Q_INVOKABLE void start();
    Q_INVOKABLE void refreshState();
    Q_INVOKABLE void selectTarget(const QString &platform);
    Q_INVOKABLE void sendMessage(const QString &text);
    Q_INVOKABLE void updateTitle(const QString &title);
    Q_INVOKABLE void updateCategory(const QString &category);
    Q_INVOKABLE void ban(const QString &platform, const QString &user);
    Q_INVOKABLE void timeout(const QString &platform, const QString &user, const QString &duration);
    Q_INVOKABLE void clearRemote(const QString &platform, int days);
    Q_INVOKABLE void clearLocal();
    Q_INVOKABLE void openStream(const QString &platform);
    Q_INVOKABLE void inspect(const QString &kind);
    Q_INVOKABLE void shutdown();
    Q_INVOKABLE void adjustFont(int steps);
    Q_INVOKABLE void resetFont();
    Q_INVOKABLE void configureConnection(const QString &mode, const QString &serverUrl);

signals:
    void stateChanged();
    void messagesChanged();
    void noticeChanged();
    void connectedChanged();
    void busyChanged();
    void dialogChanged();
    void messageSent();
    void fontScaleChanged();
    void connectionChanged();

private:
    using JsonCallback = std::function<void(const QVariantMap &)>;

    QUrl endpoint(const QString &path) const;
    void checkRuntime();
    void launchRuntime();
    void stopRuntime();
    void connectEvents();
    void processEventBlock(const QByteArray &block);
    void applyState(const QVariantMap &state);
    void appendMessage(QVariantMap message);
    void postAction(const QVariantMap &payload, const QString &success, const JsonCallback &callback = {});
    void getJson(const QString &path, const JsonCallback &callback);
    void postJson(const QString &path, const QVariantMap &payload, const JsonCallback &callback);
    void finishReply(QNetworkReply *reply, const JsonCallback &callback);
    void setNotice(const QString &text, bool error = false);
    void setBusy(bool value);
    void setConnected(bool value);
    void setFontScale(double value);
    static QString displayText(const QVariantMap &message);
    static QString prettyJson(const QVariant &value);

    QNetworkAccessManager m_network;
    QUrl m_baseUrl{QStringLiteral("http://127.0.0.1:8792/")};
    QNetworkReply *m_events = nullptr;
    QByteArray m_eventBuffer;
    QProcess m_runtime;
    QTimer m_runtimeProbe;
    QTimer m_reconnect;
    QVariantMap m_state;
    QVariantList m_messages;
    QString m_notice;
    QString m_dialogTitle;
    QString m_dialogText;
    bool m_noticeError = false;
    bool m_connected = false;
    bool m_busy = false;
    bool m_ownsRuntime = false;
    int m_probeAttempts = 0;
    QFont m_baseFont;
    double m_fontScale = 1.0;
    QString m_connectionMode;
    QString m_serverUrl;
};
