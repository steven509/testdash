Output scene3.mp4
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
Type "s"
Sleep 600ms
Down@250ms 4
Sleep 1.8s
Type "t"
Sleep 2.2s
Type "t"
Type "3"
Down@60ms 14
Up
Sleep 1s
Enter
Sleep 500ms
Type "z"
Sleep 2.2s
