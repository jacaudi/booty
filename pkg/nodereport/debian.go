package nodereport

import "encoding/base64"

// DebianLateCommand returns a preseed late_command that installs the
// script and unit into the target and enables the unit. Base64 contains no
// single quote, so the outer single quotes are safe.
func DebianLateCommand(script, unit string) string {
	return "in-target sh -c 'mkdir -p /opt/booty && echo " + base64.StdEncoding.EncodeToString([]byte(script)) +
		" | base64 -d > " + ScriptPath + " && chmod 0755 " + ScriptPath + " && echo " +
		base64.StdEncoding.EncodeToString([]byte(unit)) + " | base64 -d > /etc/systemd/system/" + UnitName +
		" && systemctl enable " + UnitName + "'"
}
