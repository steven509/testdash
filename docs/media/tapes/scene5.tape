Output scene5.mp4
Source common.tape
Set FontSize 19
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
Ctrl+S
Sleep 1.4s
Down@300ms 2
Sleep 500ms
Right
Sleep 1.1s
Right
Sleep 1.1s
Right
Sleep 1.1s
Right
Sleep 900ms
Up@300ms 2
Sleep 400ms
Left
Sleep 1.4s
Right
Sleep 900ms
Escape
Sleep 700ms
Ctrl+P
Sleep 500ms
Type@120ms "fail"
Sleep 1.4s
Escape
Sleep 500ms
Type "q"
Sleep 1.4s
Type "y"
Sleep 1.2s
