// Fonts ship inside the binary: this tool is used next to airgapped networks,
// so it must not reach out to a font CDN to render.
import '@fontsource-variable/ibm-plex-sans/wght.css'
import '@fontsource/ibm-plex-mono/latin-400.css'
import '@fontsource/ibm-plex-mono/latin-500.css'
import '@fontsource/ibm-plex-mono/latin-600.css'

import { createApp } from 'vue'
import App from './App.vue'

createApp(App).mount('#app')
