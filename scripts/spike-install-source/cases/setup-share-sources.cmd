@echo off
set S=192.168.77.1
set L=X:\kyro.log
echo %time% script start >>%L%
wpeinit
echo %time% wpeinit done >>%L%
wpeutil WaitForNetwork >>%L% 2>&1
echo %time% WaitForNetwork done >>%L%
:map
net use \\%S%\win11 /user:kyro kyro-spike >>%L% 2>&1
if errorlevel 1 (
	echo %time% net use failed >>%L%
	ping -n 3 127.0.0.1 >nul
	goto map
)
echo %time% share mapped >>%L%
net use \\%S%\log /user:kyro kyro-spike >>%L% 2>&1
rem
echo %time% starting: \\%S%\win11\sources\setup.exe >>%L%
copy /y %L% \\%S%\log\setup-share-sources.log >nul 2>&1
\\%S%\win11\sources\setup.exe
echo %time% setup exit code %errorlevel% >>%L%
copy /y %L% \\%S%\log\setup-share-sources.log >nul 2>&1
xcopy /s /y /i X:\Windows\Panther \\%S%\log\setup-share-sources-panther >nul 2>&1
cmd.exe
