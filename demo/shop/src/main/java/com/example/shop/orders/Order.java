package com.example.shop.orders;

import java.math.BigDecimal;
import java.util.List;

public record Order(long id, String customerId, String status, BigDecimal subtotal, BigDecimal discount,
        BigDecimal shipping, BigDecimal total, String country, String carrier, String paymentId, List<Line> lines) {

    public record Line(String sku, int qty, BigDecimal price) {
    }
}
