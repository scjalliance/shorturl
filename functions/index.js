const functions = require('firebase-functions');
const admin = require('firebase-admin');
admin.initializeApp();

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

    return db.collection(request.hostname).doc(slug).get().then(documentSnapshot => {
        if (documentSnapshot.exists) {
            let data = documentSnapshot.data();
            documentSnapshot.ref.update({
                clickCount: admin.firestore.FieldValue.increment(1),
                clickLast: admin.firestore.FieldValue.serverTimestamp()
            });
            if (data.usePaths) {
                let requestUrl = request.url.replace(/^\/[^/]+\//, "");
                // eslint-disable-next-line promise/no-nesting
                return documentSnapshot.ref.collection("paths").get().then(querySnapshot => {
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
            } else {
                return doRedirect(data, data.destination);
            }
        } else {
            if (slug === "404") {
                response.redirect("/404.html"); // the 404 slug doesn't exist, so fail us to a static 404 page
            } else {
                response.redirect("/404" + request.url); // use the 404 slug to send us somewhere
            }
        }
        return "";
    });
});
