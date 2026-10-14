Output scene3.mp4
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
Show
Sleep 600ms
Type "r"
Sleep 5s
Type "]"
Sleep 7s
Type "]"
Wait+Screen@120s /FAILED [0-9]/
Sleep 1.5s
Type "]"
Sleep 1.6s
Type "]"
Sleep 100ms
Type "]"
Sleep 2s
