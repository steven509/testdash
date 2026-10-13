package com.example.shop;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import io.cucumber.java.en.Given;
import io.cucumber.java.en.Then;
import io.cucumber.java.en.When;
import java.util.UUID;

/** The steps of checkout.feature: a cart, its checkout, and a delivery quote, through the shop's API. */
public class CheckoutSteps {

    private String cart;
    private Shop.Response response;

    @Given("a new cart")
    public void aNewCart() {
        cart = "/carts/" + UUID.randomUUID().toString().substring(0, 8);
    }

    @Given("the cart holds {int} {string} and {int} {string}")
    public void theCartHolds(int qty, String sku, int otherQty, String otherSku) {
        add(qty, sku);
        add(otherQty, otherSku);
    }

    @When("the customer adds {int} {string}")
    public void theCustomerAdds(int qty, String sku) {
        response = add(qty, sku);
    }

    @When("the customer {string} checks out")
    public void theCustomerChecksOut(String customer) {
        response = Shop.post(cart + "/checkout", Shop.json.createObjectNode().put("customerId", customer));
    }

    @When("the customer asks for delivery to {word}")
    public void theCustomerAsksForDeliveryTo(String country) {
        response = Shop.get("/shipping/quote?country=" + country);
    }

    @Then("an order is placed with {int} lines")
    public void anOrderIsPlaced(int lines) {
        assertEquals(201, response.status(), response.body().toString());
        assertEquals(lines, response.body().path("lines").size());
    }

    @Then("the cart is empty")
    public void theCartIsEmpty() {
        assertTrue(Shop.get(cart).body().path("items").isMissingNode(), "the cart is emptied");
    }

    @Then("the carrier is {}")
    public void theCarrierIs(String carrier) {
        assertEquals(200, response.status(), response.body().toString());
        assertEquals(carrier, response.body().path("carrier").asText());
    }

    @Then("the shop answers {int}")
    public void theShopAnswers(int status) {
        assertEquals(status, response.status(), response.body().toString());
    }

    private Shop.Response add(int qty, String sku) {
        return Shop.post(cart + "/items", Shop.json.createObjectNode().put("sku", sku).put("qty", qty));
    }
}
