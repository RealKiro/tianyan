; 天眼教师端 Windows 安装程序（NSIS）
; 构建（CI 内）：makensis -V2 -DSRCBIN=dist/windows-amd64/bin -DOUTFILE=dist/tianyan-teacher-setup.exe packaging/windows/teacher.nsi

!define APPNAME "天眼教师端"
!define COMPANY "TianYan"

Name "${APPNAME}"
OutFile "${OUTFILE}"
RequestExecutionLevel admin
InstallDir "$PROGRAMFILES64\TianYan"
InstallDirRegKey HKLM "Software\${COMPANY}\Teacher" "InstallDir"
ShowInstDetails show
ShowUninstDetails show

Page directory
Page instfiles
UninstPage uninstConfirm
UninstPage instfiles

Section "Install"
  SetOutPath "$INSTDIR"
  File "${SRCBIN}\teacher.exe"
  File "${SRCDOCS}\README.md"

  WriteRegStr HKLM "Software\${COMPANY}\Teacher" "InstallDir" "$INSTDIR"
  WriteUninstaller "$INSTDIR\uninstall.exe"
  CreateDirectory "$SMPROGRAMS\${COMPANY}"
  CreateShortcut "$DESKTOP\天眼教师端.lnk" "$INSTDIR\teacher.exe"
  CreateShortcut "$SMPROGRAMS\${COMPANY}\天眼教师端.lnk" "$INSTDIR\teacher.exe"
  CreateShortcut "$SMPROGRAMS\${COMPANY}\卸载教师端.lnk" "$INSTDIR\uninstall.exe"
SectionEnd

Section "Uninstall"
  ExecWait 'taskkill /F /IM teacher.exe'
  Sleep 400
  Delete "$INSTDIR\teacher.exe"
  Delete "$INSTDIR\README.md"
  Delete "$INSTDIR\uninstall.exe"
  Delete "$DESKTOP\天眼教师端.lnk"
  RMDir /r "$SMPROGRAMS\${COMPANY}"
  RMDir "$INSTDIR"
  DeleteRegKey HKLM "Software\${COMPANY}\Teacher"
SectionEnd
