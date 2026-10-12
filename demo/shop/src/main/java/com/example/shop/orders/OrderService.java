package com.example.shop.orders;

import com.example.shop.api.ApiException;
import com.example.shop.audit.AuditLog;
import com.example.shop.catalog.Product;
import com.example.shop.catalog.ProductRepository;
import com.example.shop.events.EventPublisher;
import com.example.shop.logging.Log;
import com.example.shop.payments.PaymentClient;
import com.example.shop.payments.PaymentGatewayException;
import com.example.shop.shipping.ShippingClient;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import java.math.BigDecimal;
import java.math.RoundingMode;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

public final class OrderService {
    private static final Log log = Log.get(OrderService.class);
    private static final ObjectMapper json = new ObjectMapper();
    private static final BigDecimal FREE_SHIPPING_FROM = new BigDecimal("100.00");
    private static final Map<String, BigDecimal> COUPONS = Map.of(
            "LOYALTY10", new BigDecimal("0.10"),
            "WELCOME5", new BigDecimal("0.05"));

    private final ProductRepository products;
    private final OrderRepository orders;
    private final ShippingClient shipping;
    private final PaymentClient payments;
    private final EventPublisher events;
    private final AuditLog audit;

    public OrderService(ProductRepository products, OrderRepository orders, ShippingClient shipping,
            PaymentClient payments, EventPublisher events, AuditLog audit) {
        this.products = products;
        this.orders = orders;
        this.shipping = shipping;
        this.payments = payments;
        this.events = events;
        this.audit = audit;
    }

    public ObjectNode place(JsonNode request, String traceId) {
        String customerId = request.path("customerId").asText("guest");
        String country = request.path("country").asText("GB");
        JsonNode items = request.path("items");
        if (!items.isArray() || items.isEmpty()) {
            throw new ApiException(400, "empty_basket", "An order needs at least one item");
        }
        Log.context("customer", customerId);
        List<Order.Line> reserved = new ArrayList<>();
        try {
            BigDecimal subtotal = BigDecimal.ZERO.setScale(2);
            BigDecimal weight = BigDecimal.ZERO;
            for (JsonNode item : items) {
                String sku = item.path("sku").asText();
                int qty = item.path("qty").asInt(1);
                Product product = products.find(sku)
                        .orElseThrow(() -> new ApiException(404, "unknown_product", "There is no product " + sku));
                if (!products.reserve(sku, qty)) {
                    log.warn("Not enough stock of %s: %d wanted, %d left", sku, qty, product.stock());
                    throw new ApiException(409, "out_of_stock", sku + " is out of stock");
                }
                reserved.add(new Order.Line(sku, qty, product.price()));
                subtotal = subtotal.add(product.price().multiply(BigDecimal.valueOf(qty)));
                weight = weight.add(product.weightKg().multiply(BigDecimal.valueOf(qty)));
            }
            BigDecimal discount = discount(request.path("couponCode").asText(null), subtotal);
            BigDecimal discounted = subtotal.subtract(discount);
            ShippingClient.Quote quote = shipping.quote(country, weight, traceId);
            BigDecimal shippingCost = discounted.compareTo(FREE_SHIPPING_FROM) >= 0 ? BigDecimal.ZERO.setScale(2) : quote.price();
            BigDecimal total = discounted.add(shippingCost);
            PaymentClient.Payment payment = payments.authorise(total, request.path("paymentSource").asText("tok_visa"),
                    "basket-" + customerId, traceId);
            Order order = new Order(0, customerId, "CONFIRMED", subtotal, discount, shippingCost, total, country,
                    quote.carrier(), payment.id(), reserved);
            long id = orders.insert(order);
            Log.context("order", id);
            events.publish("orders.events", String.valueOf(id), "OrderPlaced", traceId, json.createObjectNode()
                    .put("orderId", id)
                    .put("customerId", customerId)
                    .put("total", total.toPlainString())
                    .put("currency", "GBP")
                    .set("lines", lines(reserved)));
            audit.record("order.placed", id, customerId, traceId);
            log.info("Order %d placed: %d items, £%s, shipping with %s", id, reserved.size(), total.toPlainString(),
                    quote.carrier());
            return view(new Order(id, customerId, order.status(), subtotal, discount, shippingCost, total, country,
                    quote.carrier(), payment.id(), reserved));
        } catch (RuntimeException e) {
            reserved.forEach(line -> products.release(line.sku(), line.qty()));
            throw e;
        }
    }

    public ObjectNode get(long id) {
        return view(orders.find(id).orElseThrow(() -> new ApiException(404, "unknown_order", "There is no order " + id)));
    }

    public ObjectNode cancel(long id, String traceId) {
        Order order = orders.find(id).orElseThrow(() -> new ApiException(404, "unknown_order", "There is no order " + id));
        Log.context("order", id);
        if (order.status().equals("CANCELLED")) {
            throw new ApiException(409, "already_cancelled", "Order " + id + " is already cancelled");
        }
        try {
            payments.refund(order.paymentId(), order.total(), traceId);
        } catch (PaymentGatewayException e) {
            log.error("Could not cancel order " + id, e);
            throw new ApiException(502, "refund_failed", e.getMessage());
        }
        orders.updateStatus(id, "CANCELLED");
        order.lines().forEach(line -> products.restock(line.sku(), line.qty()));
        events.publish("orders.events", String.valueOf(id), "OrderCancelled", traceId,
                json.createObjectNode().put("orderId", id));
        audit.record("order.cancelled", id, order.customerId(), traceId);
        log.info("Order %d cancelled and refunded", id);
        return get(id);
    }

    private BigDecimal discount(String couponCode, BigDecimal subtotal) {
        if (couponCode == null) {
            return BigDecimal.ZERO.setScale(2);
        }
        BigDecimal rate = COUPONS.get(couponCode);
        if (rate == null) {
            log.warn("Ignoring unknown coupon code '%s'", couponCode);
            return BigDecimal.ZERO.setScale(2);
        }
        return subtotal.multiply(rate).setScale(2, RoundingMode.HALF_UP);
    }

    private static ArrayNode lines(List<Order.Line> lines) {
        ArrayNode array = json.createArrayNode();
        lines.forEach(line -> array.addObject().put("sku", line.sku()).put("qty", line.qty()));
        return array;
    }

    private static ObjectNode view(Order order) {
        ObjectNode view = json.createObjectNode()
                .put("id", order.id())
                .put("customerId", order.customerId())
                .put("status", order.status())
                .put("subtotal", order.subtotal().toPlainString())
                .put("discount", order.discount().toPlainString())
                .put("shipping", order.shipping().toPlainString())
                .put("total", order.total().toPlainString())
                .put("country", order.country())
                .put("carrier", order.carrier())
                .put("paymentId", order.paymentId());
        view.set("lines", lines(order.lines()));
        return view;
    }
}
