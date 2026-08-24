package compile

const powerShellSyntaxCheckCommand = `& { param($path) $tokens=$null; $errors=$null; [System.Management.Automation.Language.Parser]::ParseFile($path,[ref]$tokens,[ref]$errors) > $null; if ($errors.Count) { $errors | ForEach-Object { [Console]::Error.WriteLine($_) }; exit 1 } }`

var powerShellEnvironment = []string{
	"HOME=/var/empty",
	"XDG_CONFIG_HOME=/var/empty/.config",
	"XDG_DATA_HOME=/var/empty/.local/share",
	"PSModulePath=/opt/microsoft/powershell/7/Modules",
	"POWERSHELL_TELEMETRY_OPTOUT=1",
	"POWERSHELL_UPDATECHECK=Off",
	"TERM=dumb",
	"NO_COLOR=1",
}
