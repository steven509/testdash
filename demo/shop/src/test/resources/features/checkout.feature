Feature: Checkout
  Customers fill a cart, check it out into an order and have it delivered.

  Background:
    Given a new cart

  Scenario: A cart is checked out into an order
    Given the cart holds 1 "KB-101" and 2 "CB-005"
    When the customer "c_jo" checks out
    Then an order is placed with 2 lines
    And the cart is empty

  Scenario Outline: Delivery is quoted for the customer's country
    When the customer asks for delivery to <country>
    Then the carrier is <carrier>

    Examples:
      | country | carrier    |
      | GB      | Royal Mail |
      | FR      | Colissimo  |
      | DE      | DHL        |

  Rule: A cart only takes what the shop sells

    Scenario: An unknown product is turned away
      When the customer adds 1 "XX-000"
      Then the shop answers 404

    Scenario: A product the shop sells goes in the cart
      When the customer adds 2 "MP-030"
      Then the shop answers 201
