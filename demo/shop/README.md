# Demo shop

A small shop service with black-box tests that uses every kind of service testdash follows, to try testdash on
and to record its screenshots and trailer (see `docs/media`). `./gradlew test` builds the application, starts it
in Docker with its services, and runs its tests against it. To watch, run `testdash` here and press `r`.

| Service | What the shop does with it |
|---|---|
| WireMock | Takes payments and quotes shipping |
| Kafka | Publishes order, inventory and customer events |
| MariaDB | Keeps the products, their stock and the orders |
| Postgres | Issues a VAT invoice for each order, with prepared statements in a transaction |
| MongoDB | Keeps the customers and an audit log |
| Redis | Keeps carts, and caches products in front of MariaDB; it has a password |

Most tests are JUnit 5 classes; `src/test/resources/features/checkout.feature` is Cucumber, with a background, a
scenario outline and a rule, run through a JUnit Platform suite (`RunCucumberTests`) with Cucumber's long names.

Three tests fail on purpose, each for a reason testdash shows:

- `applies a loyalty discount`: the shop ignores coupon codes in lower case, and logs a warning saying so.
- `refunds the payment when cancelled`: the shop sends the refund as `text/plain`, so no WireMock stub matches.
- `JP ships with Japan Post`: the shipping stub for Japan answers 503, and the shop gives up after three tries.
