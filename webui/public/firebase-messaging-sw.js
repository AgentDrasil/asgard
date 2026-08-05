importScripts("https://www.gstatic.com/firebasejs/10.8.0/firebase-app-compat.js");
importScripts("https://www.gstatic.com/firebasejs/10.8.0/firebase-messaging-compat.js");

firebase.initializeApp({
  apiKey: "AIzaSyD7NMrg6mFHmj4JhmNfxZWIDscKaZmfO5c",
  authDomain: "asgard-webpush.firebaseapp.com",
  projectId: "asgard-webpush",
  storageBucket: "asgard-webpush.firebasestorage.app",
  messagingSenderId: "134748411039",
  appId: "1:134748411039:web:0c1fdc3b5ac8be9f45beaa",
});

const messaging = firebase.messaging();

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
