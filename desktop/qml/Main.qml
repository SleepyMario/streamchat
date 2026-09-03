import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

ApplicationWindow {
    id: window
    visible: true
    width: 1280
    height: 800
    minimumWidth: 900
    minimumHeight: 600
    title: "Streamchat 3.4"
    color: "#080b12"
    font.pixelSize: window.scaled(13)

    palette {
        window: "#080b12"
        windowText: "#eef2fa"
        base: "#090d15"
        alternateBase: "#111723"
        text: "#eef2fa"
        button: "#161d2b"
        buttonText: "#eef2fa"
        highlight: "#746bf2"
        highlightedText: "#ffffff"
        placeholderText: "#738096"
    }

    readonly property color surface: "#101521"
    readonly property color line: "#252d3d"
    readonly property color muted: "#8994a8"
    readonly property color accent: "#746bf2"
    readonly property string selectedTarget: String(streamchat.state.selected || "")
    property string chatFilter: "all"
    function scaled(size) { return Math.round(size * streamchat.fontScale) }

    Shortcut {
        sequences: ["Ctrl++", "Ctrl+="]
        onActivated: streamchat.adjustFont(1)
    }
    Shortcut {
        sequence: "Ctrl+-"
        onActivated: streamchat.adjustFont(-1)
    }
    Shortcut {
        sequence: "Ctrl+0"
        onActivated: streamchat.resetFont()
    }

    Connections {
        target: streamchat
        function onMessageSent() {
            composer.clear()
        }
        function onNoticeChanged() {
            if (streamchat.notice.length > 0) {
                toast.open()
                toastTimer.restart()
            }
        }
        function onDialogChanged() {
            if (streamchat.dialogText.length > 0)
                detailDialog.open()
        }
    }

    Timer {
        id: toastTimer
        interval: 3200
        onTriggered: toast.close()
    }

    Popup {
        id: toast
        x: Math.round((window.width - width) / 2)
        y: window.height - height - 82
        width: Math.min(520, toastText.implicitWidth + 34)
        height: toastText.implicitHeight + 24
        padding: 12
        modal: false
        closePolicy: Popup.NoAutoClose
        background: Rectangle {
            radius: 9
            color: streamchat.noticeError ? "#ff8391" : "#e8edf7"
        }
        contentItem: Label {
            id: toastText
            text: streamchat.notice
            color: streamchat.noticeError ? "#20050a" : "#10131b"
            wrapMode: Text.Wrap
        }
    }

    Dialog {
        id: detailDialog
        width: Math.min(760, window.width - 80)
        height: Math.min(620, window.height - 80)
        anchors.centerIn: parent
        modal: true
        title: streamchat.dialogTitle
        standardButtons: Dialog.Close
        ScrollView {
            anchors.fill: parent
            TextArea {
                text: streamchat.dialogText
                readOnly: true
                selectByMouse: true
                wrapMode: TextEdit.Wrap
                font.family: "Noto Sans Mono CJK TC"
                color: "#cbd4e3"
            }
        }
    }

    ColumnLayout {
        anchors.fill: parent
        spacing: 0

        Rectangle {
            Layout.fillWidth: true
            Layout.preferredHeight: 70
            color: "#0b0f18"
            border.color: window.line

            RowLayout {
                anchors.fill: parent
                anchors.leftMargin: 24
                anchors.rightMargin: 20
                spacing: 14

                Rectangle {
                    width: 38
                    height: 38
                    radius: 11
                    color: window.accent
                    Label { anchors.centerIn: parent; text: "S"; font.pixelSize: window.scaled(20); font.bold: true }
                }
                Column {
                    Label { text: "Streamchat"; font.pixelSize: window.scaled(18); font.bold: true }
                    Label { text: "2.0"; color: window.muted; font.pixelSize: window.scaled(11) }
                }
                Item { Layout.fillWidth: true }
                Rectangle {
                    width: 9; height: 9; radius: 5
                    color: streamchat.connected ? "#42d894" : "#ffba55"
                }
                Label {
                    text: streamchat.connected ? "Relay connected" : "Connecting to runtime"
                    color: window.muted
                }
                Button { text: "A−"; onClicked: streamchat.adjustFont(-1); ToolTip.visible: hovered; ToolTip.text: "Decrease text size (Ctrl+-)" }
                Button { text: Math.round(streamchat.fontScale * 100) + "%"; onClicked: streamchat.resetFont(); ToolTip.visible: hovered; ToolTip.text: "Reset text size (Ctrl+0)" }
                Button { text: "A+"; onClicked: streamchat.adjustFont(1); ToolTip.visible: hovered; ToolTip.text: "Increase text size (Ctrl++)" }
                Button { text: "Refresh"; onClicked: streamchat.refreshState() }
                Button { text: "Shut down"; onClicked: shutdownDialog.open() }
            }
        }

        RowLayout {
            Layout.fillWidth: true
            Layout.preferredHeight: 68
            spacing: 1

            Repeater {
                model: ["kick", "twitch", "youtube"]
                delegate: Rectangle {
                    required property string modelData
                    Layout.fillWidth: true
                    Layout.fillHeight: true
                    color: "#0d111b"
                    border.color: window.line
                    property var info: streamchat.state.streams ? streamchat.state.streams[modelData] || ({}) : ({})
                    property bool configured: streamchat.state.targets ? Boolean(streamchat.state.targets[modelData]) : false
                    Column {
                        anchors.fill: parent
                        anchors.margins: 12
                        spacing: 3
                        Label {
                            text: modelData.toUpperCase()
                            color: modelData === "kick" ? "#53fc18" : modelData === "twitch" ? "#a970ff" : "#ff4e45"
                            font.bold: true
                            font.pixelSize: window.scaled(11)
                        }
                        Label {
                            width: parent.width
                            elide: Text.ElideRight
                            text: info.title || info.error || (configured ? (info.live ? "Live" : "Configured") : "Setup required")
                            font.bold: true
                        }
                        Label {
                            text: info.live ? "LIVE · " + (info.viewer_count || 0) + " viewers · " + (info.category || "No category") : (info.category ? "OFFLINE · " + info.category : "")
                            color: window.muted
                            font.pixelSize: window.scaled(11)
                        }
                    }
                }
            }
        }

        RowLayout {
            Layout.fillWidth: true
            Layout.fillHeight: true
            spacing: 0

            ColumnLayout {
                Layout.fillWidth: true
                Layout.fillHeight: true
                spacing: 0

                RowLayout {
                    Layout.fillWidth: true
                    Layout.preferredHeight: 48
                    Layout.leftMargin: 18
                    Layout.rightMargin: 18
                    Button { text: "All"; checkable: true; checked: window.chatFilter === "all"; onClicked: window.chatFilter = "all" }
                    Button { text: "Kick"; checkable: true; checked: window.chatFilter === "kick"; onClicked: window.chatFilter = "kick" }
                    Button { text: "Twitch"; checkable: true; checked: window.chatFilter === "twitch"; onClicked: window.chatFilter = "twitch" }
                    Button { text: "YouTube"; checkable: true; checked: window.chatFilter === "youtube"; onClicked: window.chatFilter = "youtube" }
                    Item { Layout.fillWidth: true }
                    Button { text: "Clear view"; onClicked: streamchat.clearLocal() }
                }

                ListView {
                    id: chatList
                    Layout.fillWidth: true
                    Layout.fillHeight: true
                    Layout.margins: 14
                    spacing: 8
                    clip: true
                    model: streamchat.messages
                    onCountChanged: positionViewAtEnd()

                    delegate: Rectangle {
                        required property var modelData
                        property bool matchesFilter: window.chatFilter === "all" || modelData.platform === window.chatFilter
                        width: chatList.width
                        height: matchesFilter ? contentColumn.implicitHeight + 24 : 0
                        visible: matchesFilter
                        radius: 11
                        color: "#111723"
                        border.color: "#1e2635"

                        RowLayout {
                            anchors.fill: parent
                            anchors.margins: 12
                            spacing: 12

                            Column {
                                Layout.preferredWidth: 74
                                Label {
                                    text: String(modelData.platform || "").toUpperCase()
                                    color: modelData.platform === "kick" ? "#53fc18" : modelData.platform === "twitch" ? "#a970ff" : "#ff4e45"
                                    font.pixelSize: window.scaled(10)
                                    font.bold: true
                                }
                                Label {
                                    text: modelData.timestamp ? new Date(modelData.timestamp).toLocaleTimeString(Qt.locale(), "hh:mm") : ""
                                    color: window.muted
                                    font.pixelSize: window.scaled(11)
                                }
                            }

                            ColumnLayout {
                                id: contentColumn
                                Layout.fillWidth: true
                                spacing: 5

                                Label {
                                    Layout.fillWidth: true
                                    visible: Boolean(modelData.reply)
                                    text: modelData.reply ? "Reply to " + (modelData.reply.author_display_name || "message") + (modelData.reply.text ? ": " + modelData.reply.text : "") : ""
                                    color: window.muted
                                    font.pixelSize: window.scaled(11)
                                    wrapMode: Text.Wrap
                                }
                                RowLayout {
                                    Layout.fillWidth: true
                                    Label {
                                        text: modelData.author_display_name || "Unknown"
                                        color: /^#[0-9a-fA-F]{6}$/.test(modelData.author_color || "") ? modelData.author_color : "#eef2fa"
                                        font.bold: true
                                    }
                                    Label {
                                        text: modelData.roles ? modelData.roles.map(function(role) { return String(role).charAt(0).toUpperCase() }).join(" · ") : ""
                                        color: "#b9c4d5"
                                        font.pixelSize: window.scaled(10)
                                    }
                                    Item { Layout.fillWidth: true }
                                }
                                Label {
                                    Layout.fillWidth: true
                                    visible: text.length > 0
                                    text: modelData.display_text || ((!modelData.emotes || modelData.emotes.length === 0) ? (modelData.text || modelData.event_type || "") : "")
                                    wrapMode: Text.Wrap
                                    color: "#e4e9f2"
                                }
                                Row {
                                    spacing: 6
                                    visible: Boolean(modelData.emotes && modelData.emotes.length > 0)
                                    Repeater {
                                        model: modelData.emotes || []
                                        delegate: Image {
                                            required property var modelData
                                            source: modelData.url || ""
                                            visible: source.toString().length > 0
                                            height: 34
                                            width: Math.min(96, implicitWidth * height / Math.max(1, implicitHeight))
                                            fillMode: Image.PreserveAspectFit
                                            asynchronous: true
                                            cache: true
                                        }
                                    }
                                }
                                Label {
                                    visible: Boolean(modelData.paid && modelData.paid.display)
                                    text: modelData.paid ? modelData.paid.display || "" : ""
                                    color: "#ffd571"
                                    font.bold: true
                                }
                            }
                        }
                    }

                    Label {
                        anchors.centerIn: parent
                        visible: chatList.count === 0
                        text: "Waiting for chat\nMessages from the configured relay will appear here."
                        horizontalAlignment: Text.AlignHCenter
                        color: window.muted
                    }
                }

                Rectangle {
                    Layout.fillWidth: true
                    Layout.preferredHeight: Math.max(72, composer.implicitHeight + 28)
                    color: "#0b0f18"
                    border.color: window.line

                    RowLayout {
                        anchors.fill: parent
                        anchors.margins: 14
                        spacing: 12
                        Column {
                            Layout.preferredWidth: 100
                            Label { text: "Sending to"; color: window.muted; font.pixelSize: window.scaled(11) }
                            Label { text: window.selectedTarget.length ? window.selectedTarget.charAt(0).toUpperCase() + window.selectedTarget.slice(1) : "Choose target"; font.bold: true }
                        }
                        TextArea {
                            id: composer
                            Layout.fillWidth: true
                            Layout.maximumHeight: 120
                            font.pixelSize: window.scaled(14)
                            placeholderText: window.selectedTarget.length ? "Write a message to " + window.selectedTarget + "…" : "Choose Kick or Twitch first…"
                            wrapMode: TextEdit.Wrap
                            Keys.onPressed: function(event) {
                                if ((event.key === Qt.Key_Return || event.key === Qt.Key_Enter) && !(event.modifiers & Qt.ShiftModifier)) {
                                    streamchat.sendMessage(text)
                                    event.accepted = true
                                }
                            }
                        }
                        Button {
                            text: "Send"
                            enabled: window.selectedTarget.length > 0 && composer.text.trim().length > 0 && !streamchat.busy
                            Layout.fillHeight: true
                            onClicked: streamchat.sendMessage(composer.text)
                        }
                    }
                }
            }

            Rectangle {
                Layout.preferredWidth: 350
                Layout.fillHeight: true
                color: "#0d111b"
                border.color: window.line

                ScrollView {
                    anchors.fill: parent
                    anchors.margins: 18
                    contentWidth: availableWidth

                    ColumnLayout {
                        width: parent.width
                        spacing: 12
                        Label { text: "CONTROL DESK"; color: "#a198ff"; font.bold: true; font.pixelSize: window.scaled(10) }
                        Label { text: "Stream controls"; font.bold: true; font.pixelSize: window.scaled(19) }

                        Label { text: "Connection"; color: window.muted }
                        RowLayout {
                            ComboBox {
                                id: connectionMode
                                Layout.fillWidth: true
                                model: ["Local server", "Remote server"]
                                currentIndex: streamchat.connectionMode === "remote" ? 1 : 0
                            }
                            Button {
                                text: "Apply"
                                onClicked: streamchat.configureConnection(connectionMode.currentIndex === 0 ? "local" : "remote", remoteServer.text)
                            }
                        }
                        TextField {
                            id: remoteServer
                            Layout.fillWidth: true
                            visible: connectionMode.currentIndex === 1
                            text: streamchat.serverUrl
                            placeholderText: "wss://streamchat.example.com/relay"
                        }

                        Label { text: "Outgoing target"; color: window.muted }
                        RowLayout {
                            Button { Layout.fillWidth: true; text: "Kick"; checkable: true; checked: window.selectedTarget === "kick"; enabled: !streamchat.busy; onClicked: streamchat.selectTarget("kick") }
                            Button { Layout.fillWidth: true; text: "Twitch"; checkable: true; checked: window.selectedTarget === "twitch"; enabled: !streamchat.busy; onClicked: streamchat.selectTarget("twitch") }
                            Button {
                                id: youtubeTarget
                                Layout.fillWidth: true
                                text: "YouTube · receive only"
                                enabled: false
                                ToolTip.visible: hovered
                                ToolTip.text: "YouTube chat is currently ingested with read-only OAuth; outgoing YouTube messages are not implemented yet."
                            }
                        }

                        Label { text: "Stream title"; color: window.muted }
                        RowLayout {
                            TextField { id: titleField; Layout.fillWidth: true; placeholderText: "New stream title" }
                            Button { text: "Update"; onClicked: streamchat.updateTitle(titleField.text) }
                        }

                        Label { text: "Category"; color: window.muted }
                        RowLayout {
                            TextField { id: categoryField; Layout.fillWidth: true; placeholderText: "Name or category ID" }
                            Button { text: "Update"; onClicked: streamchat.updateCategory(categoryField.text) }
                        }

                        Label { text: "Moderation"; color: window.muted }
                        ComboBox { id: moderationPlatform; Layout.fillWidth: true; model: ["kick", "twitch"] }
                        TextField { id: moderationUser; Layout.fillWidth: true; placeholderText: "Username" }
                        RowLayout {
                            Button { text: "Ban"; onClicked: streamchat.ban(moderationPlatform.currentText, moderationUser.text) }
                            TextField { id: durationField; Layout.fillWidth: true; text: "10m"; placeholderText: "Duration" }
                            Button { text: "Timeout"; onClicked: streamchat.timeout(moderationPlatform.currentText, moderationUser.text, durationField.text) }
                        }

                        Label { text: "Remote chat"; color: window.muted }
                        RowLayout {
                            ComboBox { id: clearPlatform; Layout.fillWidth: true; model: ["kick", "twitch"] }
                            SpinBox { id: clearDays; from: 1; to: 30; value: 1; enabled: clearPlatform.currentText === "kick" }
                            Button { text: "Clear"; onClicked: streamchat.clearRemote(clearPlatform.currentText, clearDays.value) }
                        }

                        Label { text: "Open active stream"; color: window.muted }
                        RowLayout {
                            Button { text: "Kick"; onClicked: streamchat.openStream("kick") }
                            Button { text: "Twitch"; onClicked: streamchat.openStream("twitch") }
                            Button { text: "YouTube"; onClicked: streamchat.openStream("youtube") }
                        }

                        Label { text: "Diagnostics"; color: window.muted }
                        Button { Layout.fillWidth: true; text: "Configuration and health"; onClicked: streamchat.inspect("config") }
                        Button { Layout.fillWidth: true; text: "Setup and OAuth"; onClicked: streamchat.inspect("setup") }
                        Button { Layout.fillWidth: true; text: "Archive statistics"; onClicked: streamchat.inspect("archive") }
                        Item { Layout.preferredHeight: 10 }
                    }
                }
            }
        }
    }

    Dialog {
        id: shutdownDialog
        anchors.centerIn: parent
        width: Math.min(440, window.width - 40)
        title: "Shut down Streamchat?"
        modal: true
        standardButtons: Dialog.Yes | Dialog.No
        onAccepted: streamchat.shutdown()
        contentItem: Label {
            text: "The native frontend and its local runtime will stop."
            wrapMode: Text.Wrap
        }
    }
}
