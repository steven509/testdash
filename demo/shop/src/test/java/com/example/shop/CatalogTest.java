package com.example.shop;

import static org.junit.jupiter.api.Assertions.assertEquals;

import org.junit.jupiter.api.DisplayNameGeneration;
import org.junit.jupiter.api.DisplayNameGenerator;
import org.junit.jupiter.api.Test;

@DisplayNameGeneration(DisplayNameGenerator.ReplaceUnderscores.class)
class CatalogTest {

    @Test
    void lists_every_product() {
        assertEquals(12, Shop.get("/products").body().size());
    }

    @Test
    void filters_by_category() {
        assertEquals(2, Shop.get("/products?category=audio").body().size());
    }

    @Test
    void returns_a_product_by_its_sku() {
        assertEquals("Thunderbolt dock", Shop.get("/products/DK-900").body().path("name").asText());
    }

    @Test
    void returns_404_for_an_unknown_sku() {
        assertEquals(404, Shop.get("/products/NOPE-1").status());
    }
}
