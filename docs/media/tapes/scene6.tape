Output scene6.mp4
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
Type "2"
Type "g"
Down
Type "3"
Type "e"
Sleep 500ms
Show
Sleep 1.2s
Type "/"
Type@80ms "UNKNOWN_TOPIC"
Enter
Sleep 900ms
Type "N"
Sleep 2.2s
Tab
Sleep 400ms
Right
Sleep 800ms
Enter
Sleep 800ms
Escape
Sleep 2s
