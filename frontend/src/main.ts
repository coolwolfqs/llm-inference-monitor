import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import './assets/main.css'

import Overview from './views/Overview.vue'
import Inference from './views/Inference.vue'
import Compute from './views/Compute.vue'
import System from './views/System.vue'
import Hardware from './views/Hardware.vue'
import Services from './views/Services.vue'
import Alerts from './views/Alerts.vue'
import Models from './views/Models.vue'
import Engines from './views/Engines.vue'

const routes = [
  { path: '/', redirect: '/overview' },
  { path: '/overview', component: Overview },
  { path: '/inference', component: Inference },
  { path: '/compute', component: Compute },
  { path: '/system', component: System },
  { path: '/hardware', component: Hardware },
  { path: '/services', component: Services },
  { path: '/alerts', component: Alerts },
  { path: '/models', component: Models },
  { path: '/engines', component: Engines },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.mount('#app')
