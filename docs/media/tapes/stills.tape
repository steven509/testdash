Source common.tape
Set FontSize 28
Set Width 2276
Set Height 1224
Source setup.tape
Type@2ms "testdash"
Enter
Wait+Screen@300s /tests ready/
Sleep 500ms

# Picking: collapse the first classes, select a class and part of another
Down
Left
Down
Left
Down
Left
Down
Space
Down 9
Space
Space
Space
Type "g"
Show
Sleep 1s
Screenshot shots/pick.png

# Filtering
Type "/"
Type "refund"
Sleep 800ms
Screenshot shots/filter.png
Escape
Escape
Sleep 300ms

# Running everything
Type "r"
Wait+Screen@90s /4 of 6 done/
Sleep 300ms
Screenshot shots/running.png
Wait+Screen@90s /FAILED [0-9]/
Sleep 6s
Screenshot shots/finished.png

# A failed test and what happened while it ran
Type "s"
Down 4
Sleep 500ms
Type "3"
Down 30
Up
Sleep 800ms
Screenshot shots/failure.png

# The request in WireMock, zoomed
Enter
Sleep 800ms
Type "z"
Sleep 800ms
Screenshot shots/wiremock.png
Escape
Sleep 300ms

# Kafka with a message's headers
Type "2"
Type "g"
Down 3
Sleep 800ms
Type "3"
Type "h"
Sleep 800ms
Screenshot shots/kafka.png

# The application's warnings and errors
Type "2"
Type "g"
Down
Sleep 800ms
Type "3"
Type "e"
Sleep 800ms
Screenshot shots/logs.png
Type "e"

# MariaDB's queries
Type "2"
Down 3
Sleep 1s
Screenshot shots/mariadb.png
Type "z"
Sleep 300ms

# Settings, commands and keys
Ctrl+S
Sleep 800ms
Screenshot shots/settings.png
Escape
Ctrl+P
Type "run"
Sleep 800ms
Screenshot shots/palette.png
Escape
Type "?"
Sleep 800ms
Screenshot shots/help.png
Escape
Type "q"
Sleep 600ms
Screenshot shots/quit.png
Type "y"
Sleep 500ms
