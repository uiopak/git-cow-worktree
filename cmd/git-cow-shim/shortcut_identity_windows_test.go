//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestShortcutAppIdPreservesLaunch(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "..", "integrations", "windows", "Set-ShortcutAppId.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, shell := range []string{"powershell.exe", "pwsh.exe"} {
		t.Run(shell, func(t *testing.T) {
			path, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s unavailable", shell)
			}
			base := canonicalTempDir(t)
			shortcut := filepath.Join(base, "COW shortcut.lnk")
			arguments := `"folder 日本 with spaces" --updated literal $(never-run) & ;`
			icon := self + ",0"
			command := "$ErrorActionPreference='Stop'; $shell=New-Object -ComObject WScript.Shell; " +
				"$link=$shell.CreateShortcut(" + psLiteral(shortcut) + "); " +
				"$link.TargetPath=" + psLiteral(self) + "; $link.Arguments=" + psLiteral(arguments) + "; " +
				"$link.WorkingDirectory=" + psLiteral(base) + "; $link.IconLocation=" + psLiteral(icon) + "; $link.Save(); " +
				"$link=$shell.CreateShortcut(" + psLiteral(shortcut) + "); $savedLaunch=@($link.TargetPath,$link.Arguments,$link.WorkingDirectory,$link.IconLocation); " +
				"$before=(Get-FileHash -LiteralPath " + psLiteral(shortcut) + ").Hash; " +
				"& " + psLiteral(script) + " -ShortcutPath " + psLiteral(shortcut) + " -AppId 'com.openai.codex' -WhatIf; " +
				"if((Get-FileHash -LiteralPath " + psLiteral(shortcut) + ").Hash -cne $before){throw 'WhatIf changed the shortcut'}; " +
				"& " + psLiteral(script) + " -ShortcutPath " + psLiteral(shortcut) + " -AppId 'com.openai.codex'; " +
				"if([CowShortcutIdentity]::Read(" + psLiteral(shortcut) + ") -cne 'com.openai.codex'){throw 'App ID was not saved'}; " +
				"$link=$shell.CreateShortcut(" + psLiteral(shortcut) + "); " +
				"$newLaunch=@($link.TargetPath,$link.Arguments,$link.WorkingDirectory,$link.IconLocation); " +
				"for($i=0;$i -lt $savedLaunch.Count;$i++){if($savedLaunch[$i] -cne $newLaunch[$i]){throw ('Identity edit changed field '+$i+': '+$savedLaunch[$i]+' -> '+$newLaunch[$i])}}; " +
				"$saved=(Get-FileHash -LiteralPath " + psLiteral(shortcut) + ").Hash; " +
				"$rejected=$false; try { & " + psLiteral(script) + " -ShortcutPath " + psLiteral(shortcut) +
				" -AppId 'invalid app id' } catch { $rejected=$true }; if(-not $rejected){throw 'Invalid app ID was accepted'}; " +
				"if((Get-FileHash -LiteralPath " + psLiteral(shortcut) + ").Hash -cne $saved){throw 'Rejected ID changed the shortcut'}"
			cmd := exec.Command(path, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
			cmd.Env = withEnv(cleanEnv(t), "PSModulePath", "")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("shortcut identity: %v\n%s", err, out)
			}
		})
	}
}
