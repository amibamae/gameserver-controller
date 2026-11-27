@echo off
pushd "%CD%"
cd "C:/Users/water/gameserver-controller"

REM Get all local branches (not on remote)
for /f "delims=" %b in ('git branch --format="%(refname:short)" ^| findstr -v "origin/"') do (
    set BRANCH=%b
    REM Check if remote exists for this branch
    if not "%BRANCH%"=="" (
        git ls-remote --heads origin | findstr /r "(^[0-9a-f]+\s+refs/heads/%BRANCH%$|^\s*missing)" >nul 2>&1
        if errorlevel 1 (
            echo UNPUBLISHED: %BRANCH%
        ) else (
            echo PUBLISHED: %BRANCH%
        )
    )
)

popd
