import { initializeApp } from "firebase/app";
import { getMessaging, getToken, onMessage } from "firebase/messaging";
import { firebaseConfig, vapidKey } from "../config/firebase";
import { registerPushToken } from "./api";

let initialized = false;

export async function initPushNotifications(): Promise<string | null> {
  if (initialized) return null;
  if (!("serviceWorker" in navigator) || !("Notification" in window)) {
    console.warn("Web Push Notifications not supported in this browser environment.");
    return null;
  }

  try {
    const permission = await Notification.requestPermission();
    if (permission !== "granted") {
      console.log("Notification permission not granted:", permission);
      return null;
    }

    const app = initializeApp(firebaseConfig);
    const messaging = getMessaging(app);

    const serviceWorkerRegistration = await navigator.serviceWorker.register(
      "/firebase-messaging-sw.js",
    );

    const token = await getToken(messaging, {
      vapidKey: vapidKey,
      serviceWorkerRegistration: serviceWorkerRegistration,
    });

    if (token) {
      await registerPushToken(token);
      initialized = true;

      // Handle foreground messages if the user is currently looking at the tab
      onMessage(messaging, (payload) => {
        const title = payload.notification?.title || payload.data?.title || "Agent Needs Input";
        const body = payload.notification?.body || payload.data?.body || "Ask-user question";
        new Notification(title, {
          body,
          icon: "/favicon.svg",
          data: payload.data || {},
        });
      });

      return token;
    } else {
      console.warn("No FCM registration token available.");
    }
  } catch (err) {
    console.error("Error initializing FCM Web Push:", err);
  }

  return null;
}
