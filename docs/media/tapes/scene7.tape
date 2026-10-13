Output scene7.mp4
Source common.tape
Set FontSize 15
Set Width 1616
Set Height 812
Set Framerate 30
Source setup.tape
Type "testdash"
Enter
Wait+Screen@300s /tests ready/
Sleep 500ms
Type "r"
Wait+Screen@120s /FAILED [0-9]/
Sleep 6s
Show
Sleep 600ms
Ctrl+P
Sleep 400ms
Type@100ms "settings"
Sleep 700ms
Enter
Sleep 1.1s
Down@250ms 2
Sleep 300ms
Right
Sleep 900ms
Right
Sleep 900ms
Right
Sleep 900ms
Right
Sleep 800ms
Up@250ms 2
Sleep 300ms
Left
Sleep 1.2s
Right
Sleep 800ms
Escape
Sleep 1.2s
