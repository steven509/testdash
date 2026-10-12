package com.example.shop;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import org.junit.jupiter.api.DisplayNameGeneration;
import org.junit.jupiter.api.DisplayNameGenerator;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;

@DisplayNameGeneration(DisplayNameGenerator.ReplaceUnderscores.class)
class ShippingQuoteTest {

    @ParameterizedTest(name = "{0} ships with {1}")
    @CsvSource({"GB, Royal Mail", "FR, Colissimo", "DE, DHL", "NL, PostNL", "US, UPS", "JP, Japan Post"})
    void quotes_each_country(String country, String carrier) {
        Shop.Response response = Shop.get("/shipping/quote?country=" + country);

        assertEquals(200, response.status(), "quote for " + country);
        assertEquals(carrier, response.body().path("carrier").asText());
    }

    @Test
    void quotes_by_weight() {
        Shop.Response response = Shop.get("/shipping/quote?country=DE&weightKg=12.5");

        assertEquals(200, response.status());
        assertTrue(response.body().path("days").asInt() > 0);
    }

    @Test
    void rejects_countries_it_cannot_ship_to() {
        Shop.Response response = Shop.get("/shipping/quote?country=AQ");

        assertEquals(422, response.status());
        assertEquals("unsupported_country", response.body().path("error").asText());
    }
}
