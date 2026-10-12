package com.example.shop.audit;

import com.mongodb.client.MongoCollection;
import com.mongodb.client.MongoDatabase;
import java.util.Date;
import org.bson.Document;

/** What happened to orders and customers, kept in MongoDB for support and compliance. */
public final class AuditLog {
    private final MongoCollection<Document> entries;

    public AuditLog(MongoDatabase database) {
        this.entries = database.getCollection("audit");
    }

    public void record(String action, Object entity, String actor, String traceId) {
        entries.insertOne(new Document("action", action)
                .append("entity", String.valueOf(entity))
                .append("actor", actor)
                .append("traceId", traceId)
                .append("at", new Date()));
    }
}
