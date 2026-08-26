@echo off
REM Double-click this file to remove WhatsApp Assistant from your PC.
REM Your message history is kept unless you choose to delete it.

setlocal
set "BIN=%LOCALAPPDATA%\WhatsAppAssistant\bin\whatsapp-assistant.exe"

echo.
if not exist "%BIN%" (
    echo WhatsApp Assistant does not appear to be installed on this PC.
    echo (Nothing found at: %BIN%^)
    echo.
    echo If you installed the Claude extension, you can still remove it from
    echo Claude ^> Settings ^> Extensions.
    echo.
    pause
    exit /b 0
)

REM Windows will not delete a running program, and the uninstaller lives inside
REM the very folder it has to remove. So run a copy from the temp folder
REM instead, leaving the installed folder free to be deleted.
set "TMPBIN=%TEMP%\whatsapp-assistant-uninstall.exe"
copy /y "%BIN%" "%TMPBIN%" >nul
if errorlevel 1 (
    echo Could not prepare the uninstaller. Please close Claude and try again.
    echo.
    pause
    exit /b 1
)

"%TMPBIN%" uninstall %*

REM The copy has exited by now, so it can be cleaned up.
del /q "%TMPBIN%" >nul 2>&1

echo.
pause
