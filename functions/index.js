const functions = require('firebase-functions');
const admin = require('firebase-admin');
const fetch = require('node-fetch');
admin.initializeApp();

const VERSION = 202103010357;
const db = admin.firestore();

exports.redir = functions.https.onRequest((request, response) => {
    let slug = request.url.replace(/^\//, "").split(/(\/|\?)/, 2)[0].toLocaleLowerCase();
    let query = "";
    {
        let q = request.url.split("?");
        q.shift();
        query = q.join("?");
    }

    let doRedirect = (data, destination) => {
        if (data.passQueryString && query !== "") {
            destination = destination + (RegExp("\\?").test(destination) ? "&" : "?") + query;
        }
        if (data.frame) {
            response
            .contentType("html")
            .send(`<html><head><title>${data.frame}</title></head><body style="padding:0;margin:0;width:100%;height:100%"><iframe style="border:0;width:100%;height:100%" title="${data.frame}" src="${destination}"/></body></html>`);
        } else {
            response.redirect(data.statusCode ? data.statusCode : 307, destination);
        }
    };

    return db.collection(request.hostname).doc(slug).get()
    .then(documentSnapshot => {
        if (documentSnapshot.exists) {
            let data = documentSnapshot.data();
            documentSnapshot.ref.update({
                clickCount: admin.firestore.FieldValue.increment(1),
                clickLast: admin.firestore.FieldValue.serverTimestamp()
            });

            if (data.passthrough) { // simple single-path proxy
                let destination = data.destination;
                if (data.passQueryString && query !== "") {
                    destination = destination + (RegExp("\\?").test(destination) ? "&" : "?") + query;
                }

                let headers = {};
                const headerValue = (key, def) => {
                    let index = request.rawHeaders.findIndex(el => el.toLowerCase() === key.toLowerCase());
                    if (index === -1) return (def || "");
                    return (request.rawHeaders[index+1] || "");
                };
                const createHeader = (key, val) => {
                    headers[key] = val;
                };
                const reuseOrCreateHeader = (key, def, emptyOk) => {
                    let val = headerValue(key, def);
                    if (!emptyOk && !val) return;
                    createHeader(key, val);
                };
                reuseOrCreateHeader("Accept", "*/*");
                reuseOrCreateHeader("Accept-Encoding");
                reuseOrCreateHeader("Accept-Language");
                reuseOrCreateHeader("Cache-Control");
                reuseOrCreateHeader("Pragma");
                reuseOrCreateHeader("Authorization");
                reuseOrCreateHeader("User-Agent");
                reuseOrCreateHeader("X-Forwarded-For", request.ip);
                createHeader("X-Passthrough-Domain", request.hostname);
                createHeader("X-Passthrough-Slug", slug);
                createHeader("X-ShortUrl-Ver", VERSION);

                functions.logger.log("[VERSION]", VERSION);

                // eslint-disable-next-line promise/no-nesting
                fetch(destination, {
                    headers: headers,
                    method: request.method,
                    body: request.rawBody
                })
                .then(res => {
                    response.set("x-shorturl-ver", VERSION);
                    if (res.ok || data.passthroughAnyStatus) return res;
                    throw new Error(res.statusText);
                })
                .then(res => {
                    for (const [key] of Object.entries(res.headers.raw())) {
                        if([
                            "access-control-allow-origin",
                            "cache-control",
                            "content-type",
                            "pragma"
                        ].includes(key.toLowerCase())) response.set(key, res.headers.get(key));
                    }
                    response.status(res.status);
                    response.send(res.buffer());
                    response.end();
                    return res;
                })
                .catch(err => {
                    functions.logger.log("passthrough error: ", err);
                    response.status(500).send(err);
                });

            } else if (data.usePaths) { // fancy regex-based path redirection
                let requestUrl = request.url.replace(/^\/[^/]+\//, "");
                // eslint-disable-next-line promise/no-nesting
                return documentSnapshot.ref.collection("paths").get()
                .then(querySnapshot => {
                    let destination = "";
                    querySnapshot.forEach(destSnapshot => {
                        if (destination !== "" || !destSnapshot.exists) return;
                        let destData = destSnapshot.data();
                        let match = RegExp(destData.pattern).exec(requestUrl);
                        if (match && match.groups !== undefined) {
                            destSnapshot.ref.update({
                                matchCount: admin.firestore.FieldValue.increment(1),
                                matchLast: admin.firestore.FieldValue.serverTimestamp()
                            });
                            destination = destData.destination;
                            for (let key in match.groups) {
                                destination = destination.split(`\${${key}}`).join(match.groups[key]);
                            }
                            return;
                        }
                    });
                    return doRedirect(data, destination ? destination : data.destination);
                });

            } else { // usual redirect
                return doRedirect(data, data.destination);
            }

        } else { // slug didn't match a document
            if (slug === "404") { // we tried to find a 404 redirect, but even it didn't exist
                response.redirect("/404.html"); // ...so fail us to a static 404 page
            } else {
                response.redirect("/404" + request.url); // use the 404 slug to send us somewhere, maybe?
            }
        }

        return undefined;
    });
});