importScripts("https://www.gstatic.com/firebasejs/10.8.0/firebase-app-compat.js");
importScripts("https://www.gstatic.com/firebasejs/10.8.0/firebase-messaging-compat.js");

let messaging = null;

async function initSwFirebase() {
  try {
    const res = await fetch("/api/config");
    if (!res.ok) return;
    const data = await res.json();
    if (!data.firebase_webpush_web) return;

    const cfg = data.firebase_webpush_web;
    const firebaseConfig = {
      apiKey: cfg.apiKey,
      authDomain: cfg.authDomain,
      projectId: cfg.projectId,
      storageBucket: cfg.storageBucket,
      messagingSenderId: cfg.messagingSenderId,
      appId: cfg.appId,
    };
    if (!firebaseConfig.apiKey) return;

    firebase.initializeApp(firebaseConfig);
    messaging = firebase.messaging();

    messaging.onBackgroundMessage((payload) => {
      console.log("[firebase-messaging-sw.js] Received background message ", payload);
      const notificationTitle =
        payload.notification?.title || payload.data?.title || "Agent Needs Input";
      const notificationOptions = {
        body: payload.notification?.body || payload.data?.body || "Ask-user question",
        icon: "/favicon.svg",
        data: payload.data || {},
      };

      self.registration.showNotification(notificationTitle, notificationOptions);
    });
  } catch (err) {
    console.error("[firebase-messaging-sw.js] Error fetching config:", err);
  }
}

initSwFirebase();

self.addEventListener("push", (event) => {
  if (!event.data) return;
  try {
    const payload = event.data.json();
    const notificationTitle =
      payload.notification?.title || payload.data?.title || "Agent Needs Input";
    const notificationOptions = {
      body: payload.notification?.body || payload.data?.body || "Ask-user question",
      icon: "/favicon.svg",
      data: payload.data || {},
      tag: payload.data?.chatID || "ask-user",
    };
    event.waitUntil(self.registration.showNotification(notificationTitle, notificationOptions));
  } catch (err) {
    console.error("[firebase-messaging-sw.js] Error parsing push payload:", err);
  }
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const chatID = event.notification.data?.chatID;
  const targetUrl = chatID ? `/chat/${chatID}` : "/";

  event.waitUntil(
    clients.matchAll({ type: "window", includeUncontrolled: true }).then((windowClients) => {
      for (const client of windowClients) {
        if (client.url.includes(targetUrl) && "focus" in client) {
          return client.focus();
        }
      }
      if (clients.openWindow) {
        return clients.openWindow(targetUrl);
      }
    }),
  );
});
