// Realtime WebSocket connection manager mirroring the legacy behaviour
export class RealtimeClient {
  constructor() {
    this.ws = null;
    this.subscriptions = new Map(); // topic -> Set<handler>
    this.reconnectDelay = 1000;
    this.maxReconnectDelay = 30000;
    this.currentDelay = this.reconnectDelay;
    this.reconnectTimer = null;
    this.pingTimer = null;
    this.connected = false;
    this.shouldReconnect = true;

    this.handleOpen = () => {
      this.connected = true;
      this.currentDelay = this.reconnectDelay;
      this.updateConnectionIndicator(true);
      this.startPingLoop();
      const topics = this.topics();
      if (topics.length) {
        this.send({ action: "subscribe", topics });
        this.requestSnapshots(topics);
      }
    };

    this.handleMessage = (event) => {
      let parsed;
      try {
        parsed = JSON.parse(event.data);
      } catch (error) {
        console.error("Failed to parse realtime payload", error);
        return;
      }

      const { type, topic, payload, message } = parsed || {};
      if (!type) return;

      switch (type) {
        case "event":
        case "snapshot":
          if (!topic) return;
          this.emit(topic, payload, { type });
          break;
        case "error":
          console.error("Realtime error:", message || payload);
          break;
        default:
          break;
      }
    };

    this.handleClose = () => {
      this.connected = false;
      this.updateConnectionIndicator(false);
      this.stopPingLoop();
      this.ws = null;
      if (this.shouldReconnect) {
        this.scheduleReconnect();
      }
    };

    this.handleError = (error) => {
      console.error("Realtime socket error", error);
    };
  }

  connect() {
    this.shouldReconnect = true;
    if (
      this.ws &&
      (this.ws.readyState === WebSocket.OPEN ||
        this.ws.readyState === WebSocket.CONNECTING)
    ) {
      return;
    }
    this.clearReconnectTimer();
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const wsURL = `${protocol}//${window.location.host}/api/realtime`;
    try {
      this.ws = new WebSocket(wsURL);
      this.ws.addEventListener("open", this.handleOpen);
      this.ws.addEventListener("message", this.handleMessage);
      this.ws.addEventListener("close", this.handleClose);
      this.ws.addEventListener("error", this.handleError);
    } catch (error) {
      console.error("Failed to establish realtime connection", error);
      this.scheduleReconnect();
    }
  }

  disconnect() {
    this.shouldReconnect = false;
    this.clearReconnectTimer();
    this.stopPingLoop();
    if (this.ws) {
      try {
        this.ws.close();
      } catch (error) {
        console.error("Failed to close realtime socket", error);
      }
      this.ws = null;
    }
  }

  subscribe(topic, handler, options = {}) {
    const key = typeof topic === "string" ? topic.trim() : "";
    if (!key || typeof handler !== "function") {
      return () => {};
    }

    let handlers = this.subscriptions.get(key);
    const isNewTopic = !handlers;
    if (!handlers) {
      handlers = new Set();
      this.subscriptions.set(key, handlers);
    }
    handlers.add(handler);

    if (this.connected) {
      if (isNewTopic) {
        this.send({ action: "subscribe", topics: [key] });
      }
      if (options.snapshot !== false) {
        this.requestSnapshots([key]);
      }
    }

    return () => this.unsubscribe(key, handler);
  }

  unsubscribe(topic, handler) {
    const key = typeof topic === "string" ? topic.trim() : "";
    if (!key) return;
    const handlers = this.subscriptions.get(key);
    if (!handlers) return;
    if (handler) {
      handlers.delete(handler);
    } else {
      handlers.clear();
    }
    if (handlers.size === 0) {
      this.subscriptions.delete(key);
      if (this.connected) {
        this.send({ action: "unsubscribe", topics: [key] });
      }
    }
  }

  emit(topic, payload, meta) {
    const handlers = this.subscriptions.get(topic);
    if (!handlers || handlers.size === 0) return;
    handlers.forEach((handler) => {
      try {
        handler(payload, meta || {});
      } catch (error) {
        console.error(`Realtime handler error for topic ${topic}`, error);
      }
    });
  }

  send(message) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      return;
    }
    try {
      this.ws.send(JSON.stringify(message));
    } catch (error) {
      console.error("Failed to send realtime message", error);
    }
  }

  requestSnapshots(topics) {
    const list = Array.isArray(topics) ? topics : [topics];
    for (const topic of list) {
      if (!topic) continue;
      this.send({ action: "snapshot", topic });
    }
  }

  startPingLoop() {
    this.stopPingLoop();
    this.pingTimer = setInterval(() => {
      this.send({ action: "ping" });
    }, 25000);
  }

  stopPingLoop() {
    if (this.pingTimer) {
      clearInterval(this.pingTimer);
      this.pingTimer = null;
    }
  }

  scheduleReconnect() {
    if (this.reconnectTimer || !this.shouldReconnect) {
      return;
    }
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.connect();
      this.currentDelay = Math.min(
        this.currentDelay * 2,
        this.maxReconnectDelay
      );
    }, this.currentDelay);
  }

  clearReconnectTimer() {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
  }

  topics() {
    return Array.from(this.subscriptions.keys());
  }

  updateConnectionIndicator(connected) {
    const liveDot = document.getElementById("liveDot");
    if (!liveDot) return;
    if (connected) {
      liveDot.classList.add("ok");
    } else {
      liveDot.classList.remove("ok");
    }
  }
}

export const realtimeClient = new RealtimeClient();
