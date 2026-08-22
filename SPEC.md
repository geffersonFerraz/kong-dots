# Spec: Kong Visual Manager

GUI visual estilo Node-RED para gerenciar múltiplas instâncias do Kong API Gateway (multi-cluster/multi-server).

## 1. Visão Geral

Ferramenta web onde cada Kong (self-hosted, diferentes servidores/clusters) é representado como um **workspace** independente. Dentro de cada workspace, o usuário vê e edita a topologia do Kong (Services, Routes, Plugins, Consumers, Upstreams) num canvas de nós conectados por linhas — igual ao paradigma do Node-RED — em vez de formulários/tabelas como no Kong Manager.

## 2. Objetivos

- Visualizar de forma imediata as relações Service → Route → Plugin → Consumer de um Kong.
- Editar essa topologia arrastando/conectando nós, sem escrever YAML ou curl na Admin API.
- Gerenciar **N Kongs diferentes** (servidores distintos, cada um com sua própria Admin API/URL/credencial) a partir de uma única interface, trocando de workspace facilmente.
- Permitir revisar (diff) antes de aplicar mudanças reais no Kong.
- Não ser só um viewer (como o KongMap): precisa suportar criação/edição real.

## 3. Não-objetivos (fora do MVP)

- Substituir decK como ferramenta de CI/CD declarativo (a ferramenta deve ser *compatível* com o formato decK, não competir com ele).
- Gerenciar Kubernetes Ingress Controller diretamente (fica para uma fase futura).
- Multi-tenant/multi-usuário com RBAC complexo (fica para v2). Existe uma separação de papéis simples — quem pode aplicar e quem só pode propor —, mas ela depende de um nome declarado pelo browser e de um token compartilhado, não de autenticação real.

## 4. Conceito de nós (estilo Node-RED)

Cada entidade do Kong vira um nó no canvas, com portas de entrada/saída que representam a relação real da Admin API:

| Nó | Representa | Conexões possíveis |
|---|---|---|
| **Service** | upstream/API alvo | → Route(s), → Plugin(s) |
| **Route** | regra de matching (path/host/method) | ← Service, → Plugin(s) |
| **Plugin** | comportamento (auth, rate-limit, transformação, etc.) | anexado a Service, Route, Consumer ou Global |
| **Consumer** | cliente da API | → Plugin(s) (credenciais/políticas por consumidor) |
| **Upstream** | balanceador lógico | → Target(s), ← Service |
| **Target** | endereço real (host:port) de um Upstream | ← Upstream |

Arrastar um Plugin sobre um Service/Route/Consumer no canvas = anexar o plugin àquela entidade (equivalente a `POST /services/{id}/plugins`). Desconectar = remover a associação.

## 5. Requisitos Funcionais

1. **Gerenciamento de Kongs (multi-server)**
   - Cadastrar um Kong: nome, Admin API URL, tipo de auth (nenhuma / API key / RBAC token Enterprise / mTLS), ambiente (dev/staging/prod), tags.
   - Testar conexão antes de salvar.
   - Listar/trocar entre Kongs cadastrados (sidebar de workspaces).
   - Suporte a Kong OSS e Enterprise (workspaces do Enterprise mapeados como "sub-workspaces" dentro do Kong).

2. **Sincronização de estado**
   - Ao abrir um workspace, o backend faz `GET` de services/routes/plugins/consumers/upstreams/targets via Admin API e monta o grafo automaticamente (auto-layout inicial).
   - Botão "Refresh" para re-sincronizar caso algo tenha mudado fora da ferramenta.

3. **Edição visual**
   - Criar nó (Service/Route/Plugin/Consumer/Upstream/Target) via menu de contexto no canvas.
   - Editar propriedades de um nó em painel lateral (form gerado a partir do schema do plugin/entidade — Kong expõe schemas via `/schemas/plugins/{name}`).
   - Conectar/desconectar nós arrastando entre portas.
   - Deletar nó (com confirmação, mostrando o que será removido em cascata).

4. **Aplicar mudanças (deploy)**
   - Mudanças no canvas ficam em estado "rascunho" (não tocam o Kong real até confirmar).
   - Botão "Review changes" mostra um diff (criar/atualizar/remover) antes de aplicar.
   - "Apply" envia as chamadas necessárias à Admin API, em lote, com rollback best-effort se algo falhar no meio.

5. **Import/Export**
   - Exportar o grafo atual como YAML no formato decK (`kong.yaml`), para versionamento em Git.
   - Importar um YAML decK e renderizar como grafo (permite editar visualmente algo que hoje é só declarativo).

6. **Histórico**
   - Log local de quem/quando aplicou o quê (mesmo sendo uso solo, útil para auditoria e "desfazer mentalmente").

7. **Copiar um nó inteiro**
   - "Copy" em um nó leva para a área de transferência do sistema (JSON) o nó **e tudo que pertence a ele**: um Service viaja com suas Routes, os Plugins de ambos, o Upstream apontado pelo `host` e os Targets desse Upstream.
   - O fecho de cópia é propositalmente mais largo que o cascade de deleção: apagar um Service não pode levar junto um Upstream compartilhado, mas copiar um sem ele entrega, do outro lado, um Service apontando para um host que ninguém responde.
   - Colar (`Ctrl/⌘+V`) recria tudo como rascunho, com ids novos. Nomes já ocupados no Kong de destino são deslocados (`billing` → `billing-copy`), e um Upstream renomeado arrasta consigo o `host` do Service colado — senão a cópia nasce com o 409 de unicidade garantido.

8. **Trabalho simultâneo**
   - Várias pessoas podem ter o mesmo Kong aberto. O Kong é o estado compartilhado; a ferramenta não guarda cópia dele.
   - **Ninguém apaga o que nunca viu**: o canvas envia ao backend o estado desejado *e* a baseline de onde partiu. O plano distingue "o usuário removeu isto" de "outra pessoa criou isto depois que a aba abriu" — o segundo caso é relatado como intocado, nunca como delete.
   - **Ninguém sobrescreve em silêncio**: entidade que mudou no Kong depois que o canvas a leu volta como **conflito**, campo a campo, e o apply é recusado (409) até o usuário escolher recarregar ou assumir que a versão dele vence.
   - **Um apply por vez por Kong**, garantido por advisory lock no PostgreSQL — vale inclusive entre réplicas do backend.
   - **Presença ao vivo**: a barra mostra quantas pessoas estão no gateway, o nó que alguém tem aberto fica marcado, e quando uma mudança é aplicada os outros canvas recebem o aviso de que estão desatualizados.
   - **Rascunho compartilhado**: criar um nó, editar a config de um plugin, apagar um Service, colar um bundle — cada edição cai no canvas de todos os outros na hora. Nada disso encosta no Kong: continua sendo um rascunho, agora construído a várias mãos, e ainda exige Review → Apply.
     - Toda edição passa por um funil único no store, que apura as entidades alteradas e envia essa lista. Remoção viaja como `null` explícito. O servidor repassa sem olhar dentro: ele decide quem vê a edição, não o que ela significa.
     - Uma aba que abre um Kong onde já há gente **pede o rascunho atual** em vez de partir do que o Kong reporta e atropelar o trabalho alheio na primeira edição. Quem responde é a aba mais antiga, para uma sala de cinco receber uma cópia e não cinco.
     - **Refresh** e **Discard** são o caminho de volta: ambos são um "todo mundo volta ao que o Kong diz" deliberado, e ambos alcançam os outros canvas.
   - **Cursores e arrasto compartilhados**: o ponteiro de cada pessoa aparece no canvas dos outros, com o nome, e um nó arrastado se move na tela de todo mundo durante o arrasto — não só ao soltar.
     - Tudo trafega em **coordenadas de fluxo**, não de tela, para cair sobre o mesmo nó independentemente do pan/zoom de cada um.
     - São quadros efêmeros: retransmitidos direto aos outros canvas (sem passar pelo roster de presença, que remontaria a lista inteira a cada movimento de mouse), limitados a 25 por segundo, não enviados quando a pessoa está sozinha, e nunca persistidos.
     - Posição de nó é a exceção: o layout é compartilhado por Kong (`canvas_layout`), então quem arrastou grava a posição final e os demais apenas acompanham. Um quadro referente a um nó que o próprio usuário está arrastando é ignorado.

9. **Fila de aprovação (quem pode de fato tocar no Kong)**
   - Sem aprovadores configurados, todo mundo aplica direto (instalação de um operador só, comportamento original).
   - Configurados `KONGFLOW_APPROVERS` / `KONGFLOW_APPROVER_TOKEN`, o *Apply* de um editor comum **não vai ao Kong**: vira uma **change request** guardada no banco, esperando revisão.
   - A request guarda o canvas proposto **e** a baseline dele. Ao ser aberta por um aprovador, o plano é **reconstruído contra o Kong daquele momento** — não o de quando foi escrita —, de modo que uma mudança parada na fila é julgada pelo gateway de hoje, conflitos incluídos.
   - Aprovar executa sob o mesmo lock e registra no histórico o aprovador e o autor. Rejeitar/retirar não encosta no Kong.

### 5.1 Desfazer, refazer e reverter

10. **Undo/redo no canvas**
    - `Ctrl/⌘+Z` desfaz a última coisa que **você** fez; `Ctrl+Shift+Z` (ou `Ctrl+Y`) refaz. Cinquenta passos.
    - Delete em cascata, colagem e arrasto são **um passo cada**: desfazer um Service apagado traz de volta suas Routes e os plugins delas, já religados.
    - O undo é local por desenho — desfaz o seu trabalho, nunca a edição que outra pessoa acabou de fazer —, mas é publicado como qualquer edição, então o canvas de todos acompanha.
    - Limite assumido: duas pessoas editando a **mesma entidade** resolvem por último-escreve-vence, e o undo restaura a entidade inteira; desfazer sua edição num Service que alguém mexeu no meio leva a mudança dele junto.

11. **Rollback de uma aplicação já feita**
    - Todo run fica no painel de histórico e pode ser revertido: o que foi criado é apagado, o que foi atualizado volta aos valores que tinha, o que foi apagado é recriado **com o id que possuía**.
    - Só as operações que o Kong de fato aceitou são invertidas — um run que falhou no meio desfaz apenas a parte que entrou.
    - O plano é **reconstruído contra o Kong no momento do clique**, não guardado de quando o run aconteceu: o que já foi desfeito na mão fica de fora, e o que mudou desde então volta como conflito a ser aceito explicitamente.
    - Roda sob o mesmo advisory lock de qualquer apply e é **gravado no próprio histórico**, então um rollback pode ser revertido por sua vez.

## 6. Arquitetura

```
┌─────────────────────┐        ┌──────────────────────────┐        ┌─────────────┐
│   Frontend (Vue 3)   │  REST  │      Backend (Go)         │  HTTP  │  Kong #1    │
│  Vue Flow + Pinia     │◄──────►│  API + Kong Admin client  │◄──────►│  Admin API  │
│  canvas de nós         │  WS   │  Storage (PostgreSQL 18)  │        └─────────────┘
└─────────────────────┘        │                            │        ┌─────────────┐
                                 │                            │◄──────►│  Kong #2    │
                                 └──────────────────────────┘        └─────────────┘
```

### 6.1 Backend (Go)

- **API**: REST (gin) para CRUD de "conexões Kong", proxy/tradução das operações do canvas para chamadas de Admin API, fila de aprovação e rollback de aplicações já feitas.
- **Kong Admin Client**: client Go dedicado (pode usar o SDK oficial `github.com/Kong/sdk-konnect-go` ou um client HTTP fino próprio, já que a Admin API é REST simples).
- **Storage**: PostgreSQL 18 guardando:
  - conexões Kong cadastradas (URL, auth — segredo criptografado em repouso),
  - posições dos nós no canvas (layout é local à ferramenta, não existe no Kong),
  - histórico de aplicações.
- **WebSocket**: canal para progresso passo a passo de um "Apply", aviso de que o estado do Kong mudou, presença e ponteiros, e as edições do rascunho compartilhado. O servidor repassa as edições sem interpretá-las — decide quem vê, não o que significam.
- **Diff engine**: compara estado atual (obtido do Kong) vs. estado desejado (grafo editado) e gera plano de operações (create/update/delete), similar ao que o decK faz internamente.

### 6.2 Frontend (Vue.js)

- **Vue 3 + Composition API**, **Pinia** para state management (um store por workspace/Kong ativo).
- **Canvas**: [Vue Flow](https://vueflow.dev/) — biblioteca madura para editores tipo node-based (mesma categoria de uso do React Flow, que inspira boa parte de ferramentas "estilo Node-RED"). Alternativa mais pesada: fork/inspiração direta do editor do Node-RED (baseado em D3), mas Vue Flow dá produtividade muito maior.
- **Painel de propriedades**: formulário dinâmico gerado a partir do JSON Schema retornado pela Admin API de cada plugin/entidade.
- **Seletor de workspace**: lista de Kongs cadastrados, com indicador de ambiente (dev/staging/prod) e status de conexão (verde/vermelho).
- **Diff viewer**: painel mostrando lista de mudanças pendentes antes do apply (estilo `terraform plan`).

## 7. Modelo de dados (storage da ferramenta)

```
kong_connections
  id, name, admin_api_url, auth_type, auth_secret_encrypted, environment, created_at

canvas_layout
  id, kong_connection_id, entity_type, entity_id (id no Kong), pos_x, pos_y

apply_history
  id, kong_connection_id, applied_at, plan_json, result_json, status, error_message, actor

change_requests
  id, connection_id, title, status (pending|applied|rejected|failed|withdrawn),
  desired_json, baseline_json, plan_json, result_json,
  requested_by, requested_at, reviewed_by, reviewed_at, review_note, error_message
```
`change_requests` é a única tabela que guarda entidades do Kong — e guarda uma *proposta*, não o estado: o canvas que alguém quer aplicar mais a baseline de onde ele partiu. É isso que permite replanejar a proposta contra o Kong real na hora da aprovação, em vez de aplicar um diff escrito horas antes.
Todo o resto (Services, Routes, Plugins, etc.) **não é duplicado** localmente — é sempre lido ao vivo da Admin API do Kong e cacheado só em memória/sessão. Isso evita drift entre a ferramenta e a realidade.

## 8. Segurança

- Credenciais de Admin API armazenadas criptografadas (AES-GCM com chave de app, ou integração com Vault/age se já usado na infra).
- Suporte a Kong atrás de Cloudflare Tunnel/mTLS (já é o padrão da sua infra atual).
- Sem exposição pública por padrão — recomendação de rodar atrás do mesmo Cloudflare Tunnel/Zero Trust já usado nos outros serviços self-hosted.

## 9. Stack técnica sugerida

Esta seção era a sugestão inicial; abaixo está o que foi de fato construído.

| Camada | Sugerido | Construído |
|---|---|---|
| Backend | Go 1.23+, chi/echo, sqlc ou GORM | **Go 1.25, gin, `database/sql` + pgx** — sem ORM: as queries são poucas e explícitas |
| Storage | Postgres | **PostgreSQL 18** |
| Frontend | Vue 3, Vite, Pinia, Vue Flow, TailwindCSS | igual ao sugerido (Tailwind 4) |
| Auth | JWT simples ou OIDC (fase 2) | **ainda fase 2**: hoje é um nome declarado pelo browser (`X-KongFlow-Actor`) mais um token compartilhado de aprovação |
| Deploy | Docker Compose ou manifest K8s/ArgoCD | **Docker Compose** (`docker-compose.yml` e `deploy/demo.yml`) |
| Comunicação real-time | `gorilla/websocket` ou `nhooyr.io/websocket` | **`gorilla/websocket`**, carregando progresso de apply, presença, ponteiros e as edições do rascunho compartilhado |

## 10. MVP (escopo mínimo para primeira versão funcional)

1. Cadastro de 1+ Kongs e troca de workspace.
2. Leitura e render automático do grafo (Services, Routes, Plugins) — **somente leitura**.
3. Edição de Plugins existentes (é o caso de uso mais comum) via painel de propriedades.
4. Criar/editar/remover Service e Route no canvas.
5. Apply com diff simples (sem rollback *automático* em falha parcial — só log de erro; desfazer uma aplicação depois é um botão, ver §5.1).
6. Export para YAML decK.

## 11. Roadmap pós-MVP

Já entregues (eram roadmap, hoje estão no produto):

- Consumers, Upstreams e Targets como nós.
- Import de YAML decK.
- Histórico com o diff de cada aplicação, e rollback de uma aplicação já feita.
- Trabalho simultâneo: rascunho compartilhado, presença, ponteiros, undo/redo.
- Fila de aprovação separando quem propõe de quem aplica.

Ainda em aberto:

- Autenticação real (OIDC/SSO) por trás do papel de aprovador, e RBAC de verdade sobre ele.
- Relay de eventos entre réplicas (PostgreSQL `LISTEN`/`NOTIFY`).
- Rascunho compartilhado persistido no servidor — hoje ele vive nos browsers e some se todas as abas fecharem.
- Undo colaborativo de verdade (hoje é último-escreve-vence por entidade).
- Credenciais de Consumer (key-auth, JWT, ACL) como entidades editáveis.
- Suporte a Konnect (SaaS da Kong) além de instâncias self-hosted.
- Templates de nós reutilizáveis (ex: "bundle" de auth + rate-limit salvo como preset, arrastável).

## 12. Riscos e considerações

- **Drift externo**: se alguém mexer no Kong fora da ferramenta (curl, decK, Kong Manager), o canvas pode ficar desatualizado — mitigado pelo "Refresh" manual, pelo aviso via WebSocket quando um apply da própria ferramenta acontece, e pela baseline, que impede o canvas velho de apagar o que apareceu depois. Um drift feito fora da ferramenta ainda só aparece no próximo plano.
- **Papel de aprovador sem autenticação real**: hoje o ator é um nome que o browser declara; só o `KONGFLOW_APPROVER_TOKEN` autentica de fato. Antes de guardar um gateway de produção, isso precisa de SSO/OIDC ou de um proxy autenticador preenchendo `X-KongFlow-Actor`.
- **Presença e progresso são in-process**: rodar mais de uma réplica exige um relay compartilhado (`LISTEN`/`NOTIFY`) para os eventos chegarem aos browsers da outra réplica. O lock de apply e a fila já funcionam entre réplicas, por viverem no banco.
- **Schemas de plugins variam por versão do Kong**: o form dinâmico depende de `/schemas/plugins/{name}`, então precisa lidar com plugins customizados (você já usa `post-function`) e versões diferentes por Kong cadastrado (hoje: Kong CE 3.9.1).
- **Auto-layout**: primeira renderização de um Kong com muitas entidades pode ficar poluída — vale usar um algoritmo de layout em grafo (dagre, já integrável com Vue Flow) em vez de posição aleatória.
