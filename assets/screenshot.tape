# The README's screenshot: the Todos tab in demo mode, with a todo open
# beside the list. Run it with `mise run screenshot`, which builds evado
# first and puts it on the PATH. Needs vhs and the JetBrains Mono Nerd Font.
#
# vhs always writes a recording too; that goes to dist/, which git ignores.
Output dist/screenshot.gif
Require evado
Set Shell bash
Set FontFamily "JetBrainsMono Nerd Font Mono"
Set FontSize 30
Set Width 2560
Set Height 1440
Set Padding 48
Set Theme "Catppuccin Mocha"
Set TypingSpeed 70ms
Set CursorBlink false

Hide
Type "evado -demo" Enter
Wait+Screen /Everything/
Sleep 1s
# Down to the todo with a checklist.
Type "jjjjj"
Sleep 2s
Show
Screenshot assets/screenshot.png
Sleep 500ms
