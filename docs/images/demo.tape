# Record with: podman run --rm -v "$PWD":/vhs:Z -w /vhs ghcr.io/charmbracelet/vhs demo.tape
# (build a linux/amd64 ai-quota-meter into this folder first: CGO_ENABLED=0 go build -o docs/images/ ..)
Output demo.gif
Set Shell "bash"
Set FontSize 22
Set Width 1400
Set Height 600
Set Padding 24
Set TypingSpeed 55ms
Set Theme { "name": "night", "background": "#07080a", "foreground": "#c9d1d9", "cursor": "#f2cc60", "selectionBackground": "#2a2f37", "black": "#07080a", "red": "#ff4d6d", "green": "#7ee787", "yellow": "#f2cc60", "blue": "#79c0ff", "purple": "#e8893a", "cyan": "#7ee787", "white": "#c9d1d9", "brightBlack": "#2a2f37", "brightRed": "#ff4d6d", "brightGreen": "#7ee787", "brightYellow": "#f2cc60", "brightBlue": "#79c0ff", "brightPurple": "#e8893a", "brightCyan": "#7ee787", "brightWhite": "#ffffff" }
Hide
Type "cd /vhs && export PATH=/vhs:$PATH && clear"
Enter
Show
Sleep 500ms
Type "ai-quota-meter"
Sleep 400ms
Enter
Sleep 6s
Hide
Type "clear && ./session.sh"
Enter
Show
Sleep 11s
Sleep 1500ms
