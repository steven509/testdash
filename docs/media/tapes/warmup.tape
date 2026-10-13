Source common.tape
Set FontSize 19
Set Width 1616
Set Height 812
Source setup.tape
Type "testdash"
Enter
Wait+Screen@300s /tests ready/
Type "r"
Wait+Screen@300s /FAILED [0-9]/
Sleep 2s
Type "q"
Sleep 500ms
Type "y"
Sleep 1s
Show
Sleep 200ms
