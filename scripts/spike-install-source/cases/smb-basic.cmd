@echo off
rem Basic SMB check against one server: map, list, read a large file back.
set S=192.168.77.1
set G=192.168.77.2
set D=\\%G%\log\vm@VM@
set L=X:\kyro.log
wpeinit
wpeutil WaitForNetwork >nul
:maplog
net use \\%G%\log /user:kyro kyro-spike >>%L% 2>&1
if not errorlevel 1 goto go
ping -n 3 127.0.0.1 >nul
goto maplog
:go
mkdir %D% 2>nul
call :run net use \\%S%\win11 /user:kyro kyro-spike
call :run net use
call :run dir \\%S%\win11
call :run dir \\%S%\win11\sources\install.*
call :run dir /s /b \\%S%\win11\efi
echo %time% copy start >>%L%
call :run copy /b /y \\%S%\win11\sources\boot.wim %D%\boot.wim
echo %time% copy end >>%L%
call :run copy /b /y \\%S%\win11\setup.exe X:\setup-from-share.exe
echo ##### done >>%L%
copy /y %L% %D%\smb-basic.log >nul 2>&1
cmd.exe
exit /b

:run
echo. >>%L%
echo ^>^>^> %* >>%L%
%* >>%L% 2>&1
echo [exit %errorlevel%] >>%L%
copy /y %L% %D%\smb-basic.log >nul 2>&1
exit /b
