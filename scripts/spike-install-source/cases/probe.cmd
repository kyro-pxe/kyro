@echo off
rem Probe: record what a stock WinPE offers, test SMB access, then stay up.
set S=192.168.77.1
set O=192.168.77.2
set L=X:\kyro.log
wpeinit
mode COM1: BAUD=115200 PARITY=n DATA=8 STOP=1 >nul 2>&1
call :run ver
call :run wpeutil WaitForNetwork
call :run ipconfig
call :run dir X:\setup.exe X:\sources\setup*.exe
call :run where curl tar powershell iscsicli drvload dism reg net bcdedit diskpart
call :run reg query HKLM\SYSTEM\CurrentControlSet\Services\LanmanWorkstation\Parameters
call :run reg query HKLM\SOFTWARE\Policies\Microsoft\Windows\LanmanWorkstation

echo ##### T2a named account, signing required >>%L%
call :run net use \\%S%\win11 /user:kyro kyro-spike
call :run dir \\%S%\win11\sources\install.*
call :run net use

echo ##### T2b guest, no credentials >>%L%
call :run net use \\%O%\guest
echo ##### T2c guest, explicit guest user >>%L%
call :run net use \\%O%\guest /user:guest ""
call :run dir \\%O%\guest\setup.exe

echo ##### T2d named account on alternative port >>%L%
call :run net use \\%O%\guest /delete /y
call :run net use \\%O%\win11 /user:kyro kyro-spike /TCPPORT:4445
call :run net use

echo ##### T5 iSCSI initiator service >>%L%
call :run sc query msiscsi
call :run net start msiscsi
call :run sc query msiscsi

echo ##### done >>%L%
call :flush
net use \\%S%\log /user:kyro kyro-spike >nul 2>&1
copy /y %L% \\%S%\log\probe.log >nul 2>&1
cmd.exe
exit /b

:run
echo. >>%L%
echo ^>^>^> %* >>%L%
%* >>%L% 2>&1
echo [exit %errorlevel%] >>%L%
call :flush
exit /b

:flush
type %L% >COM1 2>nul
type nul >X:\flushed
exit /b
