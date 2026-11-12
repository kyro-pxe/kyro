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
for %%v in (BypassTPMCheck BypassSecureBootCheck BypassRAMCheck BypassCPUCheck BypassStorageCheck) do reg add HKLM\SYSTEM\Setup\LabConfig /v %%v /t REG_DWORD /d 1 /f >>%L% 2>&1
echo %time% starting: \\%S%\win11\setup.exe >>%L%
copy /y %L% \\%S%\log\labconfig-share.log >nul 2>&1
\\%S%\win11\setup.exe
echo %time% setup exit code %errorlevel% >>%L%
copy /y %L% \\%S%\log\labconfig-share.log >nul 2>&1
xcopy /s /y /i X:\Windows\Panther \\%S%\log\labconfig-share-panther >nul 2>&1
cmd.exe
