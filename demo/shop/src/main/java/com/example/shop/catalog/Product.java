package com.example.shop.catalog;

import java.math.BigDecimal;

public record Product(String sku, String name, String category, BigDecimal price, int stock, BigDecimal weightKg) {
}
