@echo off
rem Probe: record what a stock Windows PE offers and test SMB access.
set S=192.168.77.1
set G=192.168.77.2
set D=\\%G%\log\vm@VM@
set L=X:\kyro.log
wpeinit
call :run ver
call :run wpeutil WaitForNetwork
call :run ipconfig
call :run dir X:\setup.exe X:\sources\setup*.exe
call :run reg query HKLM\SYSTEM\CurrentControlSet\Services\LanmanWorkstation\Parameters
call :run reg query HKLM\SOFTWARE\Policies\Microsoft\Windows\LanmanWorkstation

echo ##### named account, signing required by the server >>%L%
call :run net use \\%S%\win11 /user:kyro kyro-spike
call :run dir \\%S%\win11\sources\install.*
call :run net use

echo ##### guest, no credentials >>%L%
call :run net use \\%G%\guest
call :run dir \\%G%\guest\setup.exe
call :run net use \\%G%\guest /delete /y

echo ##### named account on an alternative port >>%L%
call :run net use \\%G%\win11 /user:kyro kyro-spike /TCPPORT:4445
call :run net use

echo ##### iSCSI initiator service >>%L%
call :run net start msiscsi

echo ##### done >>%L%
net use \\%G%\log /user:kyro kyro-spike >nul 2>&1
mkdir %D% 2>nul
copy /y %L% %D%\probe.log >nul 2>&1
cmd.exe
exit /b

:run
echo. >>%L%
echo ^>^>^> %* >>%L%
%* >>%L% 2>&1
echo [exit %errorlevel%] >>%L%
exit /b
