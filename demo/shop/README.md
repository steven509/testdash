# Demo shop

A small shop service with black-box tests, for testdash's screenshots and trailer (see `docs/media`). `./gradlew test`
builds the application, starts it in Docker with WireMock (payments and shipping), Kafka, MariaDB and MongoDB, and
runs its tests against it. Three tests fail on purpose, each for a reason testdash shows:

- `applies a loyalty discount`: the shop ignores coupon codes in lower case, and logs a warning saying so.
- `refunds the payment when cancelled`: the shop sends the refund as `text/plain`, so no WireMock stub matches.
- `JP ships with Japan Post`: the shipping stub for Japan answers 503, and the shop gives up after three tries.
