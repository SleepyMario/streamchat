Unicode true

!ifndef VERSION
  !define VERSION "3.3"
!endif
!ifndef STAGE
  !error "STAGE must name the prepared Windows bundle directory"
!endif
!ifndef OUTPUT
  !define OUTPUT "Streamchat-${VERSION}-windows-x86_64.exe"
!endif

Name "Streamchat ${VERSION}"
OutFile "${OUTPUT}"
InstallDir "$LocalAppData\Programs\Streamchat"
InstallDirRegKey HKCU "Software\SleepyMario\Streamchat" "InstallDir"
RequestExecutionLevel user
SetCompressor /SOLID lzma
ShowInstDetails show
ShowUninstDetails show

VIProductVersion "3.3.0.0"
VIAddVersionKey /LANG=1033 "ProductName" "Streamchat"
VIAddVersionKey /LANG=1033 "CompanyName" "SleepyMario"
VIAddVersionKey /LANG=1033 "LegalCopyright" "Copyright 2026 SleepyMario"
VIAddVersionKey /LANG=1033 "FileDescription" "Streamchat desktop installer"
VIAddVersionKey /LANG=1033 "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=1033 "ProductVersion" "${VERSION}"

Page directory
Page instfiles
UninstPage uninstConfirm
UninstPage instfiles

Section "Streamchat" SecMain
  SetShellVarContext current
  SetOutPath "$INSTDIR"
  File /r "${STAGE}\*"

  WriteUninstaller "$INSTDIR\Uninstall.exe"
  WriteRegStr HKCU "Software\SleepyMario\Streamchat" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\Streamchat" "DisplayName" "Streamchat"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\Streamchat" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\Streamchat" "Publisher" "SleepyMario"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\Streamchat" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\Streamchat" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\Streamchat" "NoModify" 1
  WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\Streamchat" "NoRepair" 1

  CreateDirectory "$SMPROGRAMS\Streamchat"
  CreateShortcut "$SMPROGRAMS\Streamchat\Streamchat.lnk" "$INSTDIR\streamchat-gui.exe"
  CreateShortcut "$SMPROGRAMS\Streamchat\Uninstall Streamchat.lnk" "$INSTDIR\Uninstall.exe"
  CreateShortcut "$DESKTOP\Streamchat.lnk" "$INSTDIR\streamchat-gui.exe"
SectionEnd

Section "Uninstall"
  SetShellVarContext current
  Delete "$DESKTOP\Streamchat.lnk"
  RMDir /r "$SMPROGRAMS\Streamchat"
  RMDir /r "$INSTDIR"
  DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\Streamchat"
  DeleteRegValue HKCU "Software\SleepyMario\Streamchat" "InstallDir"
  ; User settings, OAuth material, and the chat archive are deliberately kept.
SectionEnd
