#include "backend.h"

#include <QGuiApplication>
#include <QDateTime>
#include <QDir>
#include <QFile>
#include <QMutex>
#include <QQmlApplicationEngine>
#include <QQmlContext>
#include <QQuickStyle>
#include <QStandardPaths>
#include <QTextStream>
#include <QUrl>

namespace {
void writeLog(QtMsgType type, const QMessageLogContext &, const QString &message)
{
    static QMutex mutex;
    QMutexLocker locker(&mutex);
    const QString directory = QStandardPaths::writableLocation(QStandardPaths::AppLocalDataLocation);
    QDir().mkpath(directory);
    QFile file(QDir(directory).filePath(QStringLiteral("streamchat-gui.log")));
    if (!file.open(QIODevice::WriteOnly | QIODevice::Append | QIODevice::Text))
        return;
    const char *level = "INFO";
    if (type == QtWarningMsg)
        level = "WARN";
    else if (type == QtCriticalMsg || type == QtFatalMsg)
        level = "ERROR";
    QTextStream(&file) << QDateTime::currentDateTime().toString(Qt::ISODateWithMs)
                       << " [" << level << "] " << message << '\n';
}
}

int main(int argc, char *argv[])
{
    QGuiApplication application(argc, argv);
    application.setApplicationName(QStringLiteral("Streamchat"));
    application.setApplicationDisplayName(QStringLiteral("Streamchat"));
    application.setOrganizationName(QStringLiteral("SleepyMario"));
    application.setDesktopFileName(QStringLiteral("com.sleepymario.streamchat"));
    qInstallMessageHandler(writeLog);

    QQuickStyle::setStyle(QStringLiteral("Fusion"));

    StreamchatBackend backend;
    QQmlApplicationEngine engine;
    engine.addImportPath(QDir(QCoreApplication::applicationDirPath()).filePath(QStringLiteral("qml")));
    engine.rootContext()->setContextProperty(QStringLiteral("streamchat"), &backend);
    QObject::connect(&engine, &QQmlApplicationEngine::objectCreationFailed,
                     &application, [] { QCoreApplication::exit(1); },
                     Qt::QueuedConnection);
    engine.load(QUrl(QStringLiteral("qrc:/qt/qml/Streamchat/qml/Main.qml")));
    if (engine.rootObjects().isEmpty())
        return 1;

    backend.start();
    return application.exec();
}
