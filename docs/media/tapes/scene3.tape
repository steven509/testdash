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
Sleep 800ms
Type "s"
Sleep 1.2s
Down@400ms 4
Sleep 2.5s
Type "3"
Sleep 400ms
Down@140ms 14
Sleep 1.8s
Up
Sleep 2.2s
Enter
Sleep 900ms
Type "z"
Sleep 3s
