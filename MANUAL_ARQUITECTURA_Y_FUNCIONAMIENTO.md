# Manual de Arquitectura, Funcionamiento y Optimización — Stardew Valley NPLN Server

Este documento detalla la arquitectura completa, el protocolo de red, los descubrimientos técnicos, las soluciones aplicadas y el mapeo exhaustivo de funciones para el servidor privado de **Stardew Valley** (Nintendo Switch) en la infraestructura de Nextendo / NPLN.

Sirve como **referencia técnica definitiva** y como **guía de optimización futura** para podar y limpiar el código Go eliminando componentes innecesarios.

---

## 1. Ficha Técnica del Juego y Entorno

- **Título:** Stardew Valley
- **Title ID:** `0100E65002BB8000`
- **Versión comprobada:** v1.6.15.13 (Update v1310720)
- **Executable Build ID (`main`):** `E7F845093E8CBC68DACF011CCB620D6667B5A20B`
- **Tenant ID Oficial:** `t-9f607adf-lp1`
- **Pila de Red del Juego:**
  - `SDK MW+Nintendo+NintendoSDK_NPLN-1_43_3-Release`
  - `SDK MW+Nintendo+PiaCommon-6_42_1-Release`
  - `SDK MW+Nintendo+PiaNpln-6_42_1-Release`
  - `SDK MW+Nintendo+NintendoSDK_gRPC_For_NPLN-0_0_0-Release`
  - `SDK MW+Nintendo+NintendoSDK_OpenSSL_For_NPLN-0_0_0-Release`

---

## 2. Flujo Completo del Protocolo (Lifecycle)

El ciclo de conexión multijugador de Stardew Valley sigue 7 fases consecutivas:

```
[Cliente Ryujinx]                                              [Nextendo NPLN Server]
       |                                                                 |
       |--- 1. NNCS Connectivity Check (UDP 101/102/103) --------------->| (nncs.go)
       |<-- NNCS NAT-type responses -------------------------------------|
       |                                                                 |
       |--- 2. TLS Handshake (SNI: t-9f607adf-lp1, ALPN: [grpc-exp, h2])->| (cert_gen.go)
       |                                                                 |
       |--- 3. RPC Auth/IssuePrearrangedUserToken ---------------------->| (auth.go)
       |<-- Access Token JWT (ES256, uid: u-XXXX, pid: XXXX) ------------|
       |                                                                 |
       |--- 4. RPC Friends/ActivateUser & SubscribeFriendUsers --------->| (friends.go)
       |<-- Friend List con Presencia & Auto-Friending ------------------|
       |                                                                 |
[HOST: Crear Granja]                                                     |
       |--- 5a. RPC GameSession/CreateGameSessionCreationTicket -------->| (matchmaking.go)
       |        config: "Farm4Player", password: false                   |
       |--- 5b. RPC GameSession/TrackGameSessionCreationTicket --------->|
       |<-- GameSession gs-XXXX creada con éxito ------------------------|
       |                                                                 |
[GUEST: Unirse]                                                          |
       |--- 6a. RPC GameSession/QueryGameSessions ---------------------->| (session_service.go)
       |<-- Lista con "Granja (HostName)" -------------------------------|
       |--- 6b. RPC GameSession/JoinGameSession ------------------------>|
       |<-- matched_user_sessions (Guest en índice 0 con token) ---------|
       |                                                                 |
[AMBOS: Gamesync & P2P Mesh]                                             |
       |--- 7a. RPC GameSession/AllocateIceServerSet ------------------->| (matchmaking.go)
       |<-- STUN (UDP 3478) + TURN (UDP 3479) ---------------------------|
       |--- 7b. RPC Gamesync/IssueToken -------------------------------->| (gamesync.go)
       |<-- Gamesync Token ----------------------------------------------|
       |--- 7c. RPC Gamesync/KeepUserSession (Bidi Streaming) ---------->|
       |--- 7d. RPC Gamesync/WriteDocuments (docs/__gs/m, __pus, __stu)->|
       |                                                                 |
       |<================= Malla P2P de PIA Establecida =================>|
       |                   ¡Partida Multijugador Activa!                 |
```

---

## 3. Descubrimientos Críticos y Soluciones Aplicadas

### A. Alineación del Tenant ID (`t-9f607adf-lp1`)
* **Problema:** En el código original de Wonder, el Tenant ID no tenía sufijo o usaba `t-9f607adf`. Stardew Valley comprueba estrictamente que las rutas de recursos comiencen con `tenants/t-9f607adf-lp1/`. De lo contrario, el SDK del cliente rechazaba la respuesta internamente.
* **Solución:** Normalización automática en `session_service.go` y configuración en `.env` de `NPLN_TENANT_ID=t-9f607adf-lp1`.

### B. Auto-Amigos Dinámico (Auto-Friending)
* **Problema:** Stardew Valley no tiene un buscador público de salas con código abierto; la búsqueda de granjas en cooperativo se basa 100% en la lista de amigos del sistema (`Friends/SubscribeFriendUsers` y `QueryGameSessions`). Si dos cuentas en emuladores locales no estaban agregadas como amigos mutuos, la lista de granjas aparecía eternamente vacía.
* **Solución:** Implementación de un grafo dinámico de auto-amigos en `friends.go`: cualquier cuenta que se activa (`ActivateUser`) pasa a ser amiga automática de todas las demás cuentas conectadas.

### C. Orden de Sesiones en `matched_user_sessions` (Error `2321-4992`)
* **Problema:** Al unirse a una granja mediante `JoinGameSession`, el SDK NPLN de Nintendo espera que el array `matched_user_sessions` corresponda 1:1 con las definiciones de usuario del llamador. Dado que el llamador envía 1 sola definición (`user_definitions[0]`), toma `matched_user_sessions[0]`.
  Anteriormente, el servidor devolvía primero al Anfitrión (con `MatchmakingIdToken` vacío `""`) y al Invitado en el índice 1. El cliente invitado tomaba el índice 0 (la sesión del anfitrión y token vacío) e intentaba llamar a `Gamesync/IssueToken`, resultando en fallo de firma ES256 y error **`2321-4992`**.
* **Solución:** En `session_service.go`, se modificó la función `matchedForCaller` para que las sesiones del llamador (`callerSessions`) se ordenen **siempre al inicio (índice 0)**, seguidas de los demás miembros.

### D. Modo Permisivo y Recuperación en `Gamesync/IssueToken`
* **Problema:** Si el token JWT de Gamesync fallaba la verificación criptográfica estricta, la sesión se abortaba.
* **Solución:** En `gamesync.go`, se añadió una ruta de recuperación en modo permisivo (`NPLN_ALLOW_UNVERIFIED=1`): si la verificación criptográfica falla pero la sesión de usuario existe y está activa en `registry.sessions`, se emite el token Gamesync legítimo.

### E. Conflicto de Constant ID en Ryujinx (Error `2318-0540`)
* **Problema:** El juego arrojaba el error `2318-0540` inmediatamente después de abrir el socket UDP `Udp/0`. 
  Investigando el SDK de Nintendo, `2318` es el módulo `nn::pia` y `0540` es **`JoinSessionFailedByDuplicateConstantId`**.
  Al desensamblar `Ryujinx.exe`, se descubrió que `ServiceAcc.GetAccountId` estaba cableado a fuego:
  ```csharp
  // Offset 0x32f6521 en Ryujinx.exe
  ldc.i4 0xCAFE // 51966 en decimal
  conv.u8
  ret
  ```
  La biblioteca `PIA` pide este ID como el identificador único constante de la consola. Al correr dos emuladores en la misma máquina con el ejecutable original, ambas consolas tenían el Constant ID `51966`, por lo que PIA rechazaba la conexión.
* **Solución:** Se parcheó el ejecutable del segundo emulador (`D:\Nextendo-emu2\Ryujinx.exe`) cambiando `0xCAFE` (51966) por `0xCAFF` (51967). Al tener IDs distintos, la conexión P2P se estableció al instante.

---

## 4. Guía Exhaustiva de Limpieza y Optimización (Used vs Unused)

Esta sección servirá como guía para podar el código Go en el futuro y dejar el servidor extremadamente ligero y exclusivo para Stardew Valley.

### Resumen por Servicio gRPC

| Servicio gRPC | Paquete Proto | Estado en Stardew Valley | Acción Recomendada |
|---|---|---|---|
| `/nn.npln.auth.v1.Auth` | `proto/auth/v1` | **ACTIVO** | Mantener solo `IssuePrearrangedUserToken`. |
| `/nn.npln.friends.v1.Friends` | `proto/friends/v1` | **ACTIVO** | Mantener `ActivateUser` y `SubscribeFriendUsers`. |
| `/nn.npln.matchmaking.v1.GameSessionService` | `proto/matchmaking/v1` | **ACTIVO** | Mantener gestión de sesiones de granja y STUN/TURN. |
| `/nn.npln.matchmaking.v1.Matchmaker` | `proto/matchmaking/v1` | **NO USADO** | **Eliminar / Desregistrar.** Stardew no usa colas públicas. |
| `/nn.npln.gamesync.v1.Gamesync` | `proto/gamesync/v1` | **ACTIVO** | Mantener `IssueToken`, `KeepUserSession`, `WriteDocuments`, `GetDocument`. |
| `/nn.npln.hydro.v1.Hydro` | `proto/hydro/v1` | **NO USADO** | **Eliminar / Desregistrar.** |
| `/nn.npln.messaging.v1.Messaging` | `proto/messaging/v1` | **NO USADO** | **Eliminar / Desregistrar.** |
| `/nn.npln.ugcstore.v1.UgcStore` | `proto/ugcstore/v1` | **NO USADO** | **Eliminar / Desregistrar.** |
| Servidor NNCS (UDP) | `nncs.go` | **ACTIVO** | Mantener para comprobación de conectividad Switch. |
| Servidor STUN (UDP) | `stun.go` | **ACTIVO** | Mantener RFC 8489. |
| Servidor TURN (UDP) | `turn.go` | **ACTIVO** | Mantener RFC 8656 relay con `pion/turn`. |

---

### Análisis Detallado Archivo por Archivo

#### 1. `main.go`
* **Usado:**
  - Inicialización del servidor gRPC con TLS en el puerto 443.
  - Registro de `Auth`, `Friends`, `GameSessionService` y `Gamesync`.
  - Inicialización de STUN (`3478`), TURN (`3479`) y NNCS (`10025`, `10125`).
* **Innecesario / Eliminar:**
  - Registro de `hydroServer` (`hydropb.RegisterHydroServer`).
  - Registro de `messagingServer` (`msgpb.RegisterMessagingServer`).
  - Registro de `ugcServer` (`ugcpb.RegisterUgcStoreServer`).
  - Flags y variables específicas de Mario Wonder.

#### 2. `auth.go`
* **Usado:**
  - `IssuePrearrangedUserToken`: Emisión de JWTs de acceso a partir del token de cuenta.
  - Generación de claves ECDSA ES256 y firma de tokens.
  - `allowUnverified()`: Soporte para cuentas en modo dev / local.
* **Innecesario / Eliminar:**
  - `IssueToken`: Autenticación legacy no utilizada por Stardew.
  - `RefreshToken`: Stardew renueva sesión volviendo a llamar `IssuePrearrangedUserToken`.
  - `RevokeToken`: No invocado.

#### 3. `friends.go`
* **Usado:**
  - `ActivateUser`: Marca al usuario actual como activo.
  - `SubscribeFriendUsers`: Stream de amigos conectados.
  - Grafo dinámico de auto-amigos (`activeUsers`, `autoFriends`).
* **Innecesario / Eliminar:**
  - `PresenceService` completo (Wonder lo usaba para estados del mapa del mundo).
  - Funciones de filtrado de presencia complejas de Wonder.

#### 4. `matchmaking.go` & `session_service.go`
* **Usado:**
  - `CreateGameSessionCreationTicket`: Creación de la granja (`Farm4Player`).
  - `TrackGameSessionCreationTicket`: Espera y resolución de la sesión creada.
  - `QueryGameSessions`: Descubrimiento de granjas creadas por amigos.
  - `JoinGameSession`: Ingreso de farmhands a la granja.
  - `AllocateIceServerSet`: Entrega de credenciales de STUN y TURN.
  - `LeaveGameSession` y `DestroyGameSession`: Desconexión limpia.
  - `matchedForCaller`: Algoritmo de ordenación de sesiones con el llamador en el índice 0.
* **Innecesario / Eliminar:**
  - Todo el struct `matchmakerServer` y sus métodos (`CreateMatchmakingTicket`, `TrackMatchmakingTicket`, `DeleteMatchmakingTicket`).
  - Lógica de pools de niveles (`FriendGameSessionId`, `FriendCoursePool`, cursos y mundos de Mario Wonder).
  - Reglas de matching aleatorio público.

#### 5. `gamesync.go`
* **Usado:**
  - `IssueToken`: Autenticación de la sesión Gamesync.
  - `KeepUserSession`: Stream bidireccional de suscripción a documentos (`targets`).
  - `WriteDocuments`: Escritura de señalización P2P (`__stu`), presencia (`__pus`) y datos de la granja (`__gs/m`, `_Pia_SystemData`).
  - `GetDocument`: Lectura de documentos de la sesión (`__gs/f`).
* **Innecesario / Eliminar:**
  - Documentos de telemetría y ghosting de Mario Wonder (`docs/__mt/course_ghost`).

#### 6. Protos Innecesarios en `proto/`
Los siguientes paquetes generados de protobuf pueden eliminarse del repositorio al optimizar:
- `proto/hydro/`
- `proto/messaging/`
- `proto/ugcstore/`

---

## 5. Configuración de Red (STUN / TURN)

El servidor incluye una implementación ligera y embebida de **STUN** (RFC 8489) y **TURN** (RFC 8656 con `github.com/pion/turn/v4`):

- **STUN:**
  - Puerto de escucha: `127.0.0.1:3478` (UDP)
  - Anunciado en `AllocateIceServerSet` como `127.0.0.1:3478`
- **TURN Relay:**
  - Puerto de escucha: `127.0.0.1:3479` (UDP)
  - Realm: `nextendo.local`
  - Usuario: `nextendo`
  - Contraseña: `stardew-local`
  - Relay IP: `127.0.0.1`

### Para partidas entre diferentes computadoras (Radmin VPN / LAN)
Si se juega entre PCs distintas en lugar de una sola máquina, basta con configurar en `.env`:
```ini
NPLN_STUN_LISTEN=0.0.0.0:3478
NPLN_STUN_HOST=<TU_IP_DE_RADMIN_O_LAN>

NPLN_TURN_LISTEN=0.0.0.0:3479
NPLN_TURN_HOST=<TU_IP_DE_RADMIN_O_LAN>
NPLN_TURN_RELAY_IP=<TU_IP_DE_RADMIN_O_LAN>

NPLN_GAMESESSION_HOST=<TU_IP_DE_RADMIN_O_LAN>
```

---

## 6. Configuración de los Clientes (Ryujinx / Consola)

1. **Parche CertBypass (IPS32):**
   - Archivo: `patches/E7F845093E8CBC68DACF011CCB620D6667B5A20B000000000000000000000000.ips`
   - Ubicación en Ryujinx: `portable/mods/contents/0100E65002BB8000/exefs/`
2. **Redirección de Dominio (`nextendo_server_override.json`):**
   ```json
   {
     "Enabled": true,
     "ServerIp": "127.0.0.1",
     "NatIp": "127.0.0.2"
   }
   ```
3. **Múltiples Instancias en la Misma PC:**
   - Crear dos carpetas separadas: `Nextendo-emu` y `Nextendo-emu2`.
   - Aplicar el parche de **Constant ID** en la segunda instancia para que devuelva `0xCAFF` (`51967`) en lugar de `0xCAFE` (`51966`).

