"use strict";
if (typeof SwaggerUIBundle === "function") {
  SwaggerUIBundle({
    url: "openapi.yaml",
    dom_id: "#swagger-ui",
    deepLinking: true,
    displayOperationId: false,
    defaultModelsExpandDepth: -1,
    docExpansion: "none",
    filter: true,
    persistAuthorization: false,
    supportedSubmitMethods: [],
    validatorUrl: null,
    syntaxHighlight: { activated: true, theme: "agate" },
    presets: [SwaggerUIBundle.presets.apis],
    layout: "BaseLayout"
  });
} else {
  const warning = document.createElement("p");
  warning.textContent = "The interactive renderer could not load. Use the OpenAPI download or API examples linked above.";
  document.getElementById("swagger-ui").replaceChildren(warning);
}
