package com.example.shop.logging;

import org.slf4j.ILoggerFactory;
import org.slf4j.IMarkerFactory;
import org.slf4j.Marker;
import org.slf4j.event.Level;
import org.slf4j.helpers.BasicMarkerFactory;
import org.slf4j.helpers.LegacyAbstractLogger;
import org.slf4j.helpers.MessageFormatter;
import org.slf4j.helpers.NOPMDCAdapter;
import org.slf4j.spi.MDCAdapter;
import org.slf4j.spi.SLF4JServiceProvider;

/** Sends the Kafka and MongoDB clients' warnings and errors to the shop's log. */
public final class ShopLoggerProvider implements SLF4JServiceProvider {
    private final ILoggerFactory loggers = ClientLogger::new;
    private final IMarkerFactory markers = new BasicMarkerFactory();
    private final MDCAdapter mdc = new NOPMDCAdapter();

    @Override
    public ILoggerFactory getLoggerFactory() {
        return loggers;
    }

    @Override
    public IMarkerFactory getMarkerFactory() {
        return markers;
    }

    @Override
    public MDCAdapter getMDCAdapter() {
        return mdc;
    }

    @Override
    public String getRequestedApiVersion() {
        return "2.0.99";
    }

    @Override
    public void initialize() {
    }

    private static final class ClientLogger extends LegacyAbstractLogger {
        private final Log log;

        ClientLogger(String name) {
            this.name = name;
            this.log = Log.get(name);
        }

        @Override
        public boolean isTraceEnabled() {
            return false;
        }

        @Override
        public boolean isDebugEnabled() {
            return false;
        }

        @Override
        public boolean isInfoEnabled() {
            return false;
        }

        @Override
        public boolean isWarnEnabled() {
            return true;
        }

        @Override
        public boolean isErrorEnabled() {
            return true;
        }

        @Override
        protected String getFullyQualifiedCallerName() {
            return null;
        }

        @Override
        protected void handleNormalizedLoggingCall(Level level, Marker marker, String pattern, Object[] args,
                Throwable error) {
            log.write(level.name(), MessageFormatter.basicArrayFormat(pattern, args), error);
        }
    }
}
