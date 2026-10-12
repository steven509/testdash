Output scene2.mp4
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
Down
Left
Down
Left
Down
Left
Type "g"
Space
Sleep 2.5s
Show
Sleep 600ms
Enter
Wait+Screen@120s /FAILED [0-9]/
Sleep 3s
