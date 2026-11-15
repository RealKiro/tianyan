; 天眼学生端 Windows 安装程序（NSIS）
; 构建（CI 内）：makensis -V2 -DSRCBIN=dist/windows-amd64/bin -DOUTFILE=dist/tianyan-student-setup.exe packaging/windows/student.nsi
; 安装动作：复制 student.exe → 运行 `student -install -daemon` 注册开机自启计划任务
; 卸载动作：结束进程 + 删除计划任务 + 删除文件（完整卸载仍建议使用 student -uninstall 以恢复 hosts）

!define APPNAME "天眼学生端"
!define COMPANY "TianYan"
!define TASKNAME "TianYanStudent"

Name "${APPNAME}"
OutFile "${OUTFILE}"
RequestExecutionLevel admin
InstallDir "$PROGRAMFILES64\TianYan"
InstallDirRegKey HKLM "Software\${COMPANY}\Student" "InstallDir"
ShowInstDetails show
ShowUninstDetails show

Page directory
Page instfiles
UninstPage uninstConfirm
UninstPage instfiles

Section "Install"
  SetOutPath "$INSTDIR"
  File "${SRCBIN}\student.exe"
  File "${SRCDOCS}\README.md"

  ; 注册开机自动后台运行（计划任务 TianYanStudent，最高权限）
  ExecWait '"$INSTDIR\student.exe" -install -daemon' $0
  DetailPrint "student -install -daemon exit code: $0"

  WriteRegStr HKLM "Software\${COMPANY}\Student" "InstallDir" "$INSTDIR"
  WriteUninstaller "$INSTDIR\uninstall.exe"
  CreateDirectory "$SMPROGRAMS\${COMPANY}"
  CreateShortcut "$SMPROGRAMS\${COMPANY}\卸载学生端.lnk" "$INSTDIR\uninstall.exe"
  CreateShortcut "$SMPROGRAMS\${COMPANY}\学生端说明.md.lnk" "$INSTDIR\README.md"
SectionEnd

Section "Uninstall"
  ; 停止运行中的客户端与守护、删除开机自启计划任务
  ExecWait 'schtasks /End /TN "${TASKNAME}"'
  ExecWait 'schtasks /Delete /F /TN "${TASKNAME}"'
  ExecWait 'taskkill /F /IM student.exe'
  Sleep 600
  Delete "$INSTDIR\student.exe"
  Delete "$INSTDIR\README.md"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  RMDir "$SMPROGRAMS\${COMPANY}"
  DeleteRegKey HKLM "Software\${COMPANY}\Student"
SectionEnd
