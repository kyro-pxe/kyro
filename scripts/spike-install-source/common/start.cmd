@echo off
rem Common start script for the Setup cases. The case file sets:
rem   CASE       name used for log files
rem   SETUP      command line that starts Setup
rem   EICFG      if defined, copy ei.cfg from the share to X:\sources first
rem   LABCONFIG  if defined, set the LabConfig bypass values first
set S=192.168.77.1
set G=192.168.77.2
set D=\\%G%\log\vm@VM@
set L=X:\kyro.log
set N=0
echo %time% script start >>%L%
wpeinit
echo %time% wpeinit done >>%L%
wpeutil WaitForNetwork >>%L% 2>&1
echo %time% WaitForNetwork done >>%L%
call %SYSTEMROOT%\System32\kyro-case.cmd

:maplog
net use \\%G%\log /user:kyro kyro-spike >>%L% 2>&1
if not errorlevel 1 goto map
ping -n 3 127.0.0.1 >nul
goto maplog

:map
mkdir %D% 2>nul
net use \\%S%\win11 /user:kyro kyro-spike >>%L% 2>&1
if not errorlevel 1 goto mapped
echo %time% net use failed >>%L%
copy /y %L% %D%\%CASE%.log >nul 2>&1
set /a N+=1
if %N% geq 15 goto done
ping -n 3 127.0.0.1 >nul
goto map

:mapped
echo %time% share mapped >>%L%
rem The file is called EI.CFG on the media. Not every server matches names
rem without regard to case, so ask for it exactly as it is stored.
if defined EICFG copy /y \\%S%\win11\sources\EI.CFG X:\sources\ei.cfg >>%L% 2>&1
if defined LABCONFIG for %%v in (BypassTPMCheck BypassSecureBootCheck BypassRAMCheck BypassCPUCheck BypassStorageCheck) do reg add HKLM\SYSTEM\Setup\LabConfig /v %%v /t REG_DWORD /d 1 /f >>%L% 2>&1
echo %time% starting: %SETUP% >>%L%
copy /y %L% %D%\%CASE%.log >nul 2>&1
%SETUP%
echo %time% setup exit code %errorlevel% >>%L%
xcopy /s /y /i X:\Windows\Panther %D%\%CASE%-panther >nul 2>&1

:done
echo %time% done >>%L%
copy /y %L% %D%\%CASE%.log >nul 2>&1
cmd.exe
