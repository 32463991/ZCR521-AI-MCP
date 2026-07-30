(() => {
  "use strict";

  const refreshIntervalMs = 10000;
  let timer = null;
  let request = null;

  const byId = (id) => document.getElementById(id);

  const pick = (source, paths, fallback = "—") => {
    for (const path of paths) {
      let value = source;
      for (const part of path.split(".")) {
        value = value && typeof value === "object" ? value[part] : undefined;
      }
      if (value !== undefined && value !== null && value !== "") {
        return value;
      }
    }
    return fallback;
  };

  const text = (id, value) => {
    byId(id).textContent = String(value);
  };

  const booleanLabel = (value) => {
    if (value === true) return "可用";
    if (value === false) return "不可用";
    return String(value ?? "—");
  };

  const durationLabel = (value) => {
    if (typeof value === "string") return value;
    if (typeof value !== "number" || !Number.isFinite(value)) return "—";
    const seconds = Math.max(0, Math.floor(value));
    const days = Math.floor(seconds / 86400);
    const hours = Math.floor((seconds % 86400) / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);
    if (days) return `${days} 天 ${hours} 小时`;
    if (hours) return `${hours} 小时 ${minutes} 分`;
    return `${minutes} 分钟`;
  };

  const render = (status) => {
    const service = String(pick(status, ["service", "service.status"], "running"));
    const state = byId("service-state");
    state.textContent = service === "running" ? "服务运行中" : service;
    state.dataset.state = service === "running" ? "running" : "error";

    text("mcp-address", pick(status, ["mcpAddress", "mcp.address"]));
    text("android-version", pick(status, ["androidVersion", "device.androidVersion", "android.version"]));
    text("root-available", booleanLabel(pick(status, ["root", "rootAvailable", "root.available"], null)));
    text("root-framework", pick(status, ["rootFramework.name", "root.framework", "rootFramework"]));
    text("module-version", pick(status, ["version", "moduleVersion", "module.version"]));
    text("workdir", pick(status, ["workDir", "paths.workDir"]));
    text("uptime", durationLabel(pick(status, ["uptimeSeconds", "uptime", "service.uptimeSeconds"], null)));
    text("task-count", pick(status, ["taskCount", "tasks.active", "tasks.count"], 0));
    text("security-warning", pick(status, ["securityWarning"], byId("security-warning").textContent));
    text("last-updated", `更新于 ${new Date().toLocaleTimeString()}`);
  };

  const renderFailure = () => {
    const state = byId("service-state");
    state.textContent = "状态不可用";
    state.dataset.state = "error";
    text("last-updated", "无法读取 /status");
  };

  const schedule = () => {
    clearTimeout(timer);
    timer = document.hidden ? null : setTimeout(refresh, refreshIntervalMs);
  };

  const refresh = async () => {
    if (document.hidden) return;
    request?.abort();
    request = new AbortController();
    try {
      const response = await fetch("/status", {
        cache: "no-store",
        credentials: "same-origin",
        signal: request.signal,
        headers: { Accept: "application/json" },
      });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      render(await response.json());
    } catch (error) {
      if (error.name !== "AbortError") renderFailure();
    } finally {
      request = null;
      schedule();
    }
  };

  document.addEventListener("visibilitychange", () => {
    if (document.hidden) {
      clearTimeout(timer);
      timer = null;
      request?.abort();
    } else {
      refresh();
    }
  });

  refresh();
})();
