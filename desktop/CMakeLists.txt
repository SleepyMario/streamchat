cmake_minimum_required(VERSION 3.21)

project(streamchat_gui VERSION 2.0 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_AUTOMOC ON)
# Streamchat only links system Qt libraries.  Avoid embedding CMake's
# build-tree RUNPATH placeholders in binaries that packaging tools copy
# directly into their final location.
set(CMAKE_SKIP_RPATH ON)

find_package(Qt6 6.5 REQUIRED COMPONENTS Core Gui Network Qml Quick QuickControls2)

qt_standard_project_setup(REQUIRES 6.5)
qt_policy(SET QTP0004 NEW)

qt_add_executable(streamchat-gui WIN32
    main.cpp
    backend.cpp
    backend.h
)

qt_add_qml_module(streamchat-gui
    URI Streamchat
    VERSION 1.0
    QML_FILES
        qml/Main.qml
)

target_link_libraries(streamchat-gui PRIVATE
    Qt6::Core
    Qt6::Gui
    Qt6::Network
    Qt6::Qml
    Qt6::Quick
    Qt6::QuickControls2
)

install(TARGETS streamchat-gui RUNTIME DESTINATION bin)
install(FILES packaging/com.sleepymario.streamchat.desktop DESTINATION share/applications)
install(FILES assets/com.sleepymario.streamchat.svg DESTINATION share/icons/hicolor/scalable/apps)
