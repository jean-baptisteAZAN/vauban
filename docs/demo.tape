# Records docs/demo.gif. Run from the repository root:
#
#   vhs docs/demo.tape
#
# docs/demo/setup.sh builds a demo vauban (stubbed cloud API) and puts fake
# tofu, gh, docker and ssh-keygen on PATH, in a temporary HOME: no real VM,
# GitHub repo or secret is touched.

Output docs/demo.gif

Set Shell bash
Set Theme "Catppuccin Mocha"
Set FontSize 14
Set Width 880
Set Height 540
Set Padding 18
Set Framerate 20
Set TypingSpeed 45ms
Set CursorBlink false

Hide
Type "export BASH_SILENCE_DEPRECATION_WARNING=1; source docs/demo/setup.sh && clear"
Enter
Sleep 12s
Show

# 1. What is there, what is missing
Type "vauban doctor"
Sleep 300ms
Enter
Sleep 4s

Hide
Type "clear"
Enter
Show

# 2. Plan: three questions, then every file and command
Type "vauban plan"
Sleep 300ms
Enter
Sleep 1.5s
# Cloud: Hetzner
Down
Sleep 500ms
Enter
# Region (fetched behind a spinner; default preselected)
Sleep 2s
Enter
# Size
Sleep 2s
Enter
# Domain: empty = <ip>.sslip.io
Sleep 1.2s
Enter
# App port: 8080 detected
Sleep 1.2s
Enter
Sleep 5s

Hide
Type "clear"
Enter
Show

# 3. Apply: confirm, spinners, done
Type "vauban apply"
Sleep 300ms
Enter
Sleep 2.5s
Left
Sleep 600ms
Enter
Sleep 12s
